package middleproxy

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	secretURL      = "https://core.telegram.org/getProxySecret"
	addressesURLV4 = "https://core.telegram.org/getProxyConfig"
	addressesURLV6 = "https://core.telegram.org/getProxyConfigV6"

	telegramHTTPTimeout  = 30 * time.Second
	telegramRefreshEvery = time.Hour
)

// telegramInfo is the middle-proxy secret and per-DC address lists fetched
// from Telegram's own well-known config endpoints - the same ones mtg v1
// and the reference C proxy poll. Addresses rotate occasionally; the secret
// essentially never does, but both are refreshed hourly to match upstream
// behavior.
type telegramInfo struct {
	mu sync.RWMutex

	secret      []byte
	v4Addresses map[DC][]string
	v6Addresses map[DC][]string
	v4Default   DC
	v6Default   DC
}

var (
	tg           = &telegramInfo{}
	tgInitOnce   sync.Once
	tgHTTPClient = &http.Client{Timeout: telegramHTTPTimeout}
)

// ensureTelegramInfo fetches the middle-proxy secret and address lists on
// first use, then keeps them refreshed hourly in the background. Safe to
// call from every connection attempt; the actual fetch only happens once
// per process.
func ensureTelegramInfo(ctx context.Context) error {
	var initErr error

	tgInitOnce.Do(func() {
		initErr = tg.refresh(ctx)
		if initErr == nil {
			go tg.backgroundRefresh()
		}
	})

	return initErr
}

func (t *telegramInfo) backgroundRefresh() {
	ticker := time.NewTicker(telegramRefreshEvery)
	defer ticker.Stop()

	for range ticker.C {
		ctx, cancel := context.WithTimeout(context.Background(), telegramHTTPTimeout)
		if err := t.refresh(ctx); err != nil {
			// Keep serving the last-known-good values; a transient fetch
			// failure an hour from now should not take down every
			// currently-open ad-tag connection.
			_ = err
		}
		cancel()
	}
}

func (t *telegramInfo) refresh(ctx context.Context) error {
	secret, err := fetchSecret(ctx)
	if err != nil {
		return fmt.Errorf("middleproxy: cannot fetch proxy secret: %w", err)
	}

	v4Addrs, v4Default, err := fetchAddresses(ctx, addressesURLV4)
	if err != nil {
		return fmt.Errorf("middleproxy: cannot fetch ipv4 proxy addresses: %w", err)
	}

	v6Addrs, v6Default, err := fetchAddresses(ctx, addressesURLV6)
	if err != nil {
		return fmt.Errorf("middleproxy: cannot fetch ipv6 proxy addresses: %w", err)
	}

	t.mu.Lock()
	t.secret = secret
	t.v4Addresses = v4Addrs
	t.v6Addresses = v6Addrs
	t.v4Default = v4Default
	t.v6Default = v6Default
	t.mu.Unlock()

	return nil
}

func (t *telegramInfo) Secret() []byte {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.secret
}

// Addresses returns candidate middle-proxy addresses for dc, IPv6 first
// then IPv4 (matching mtg v1's default PreferIP behavior) - callers should
// try them in order and fall back on failure, since Telegram occasionally
// lists an IPv6 endpoint this port has observed refusing/dropping the
// nonce exchange (live-tested: the v4 address for a given DC has always
// worked; a specific v6 one did not, on an otherwise IPv6-healthy host -
// most likely that endpoint being stale or speaking a different protocol
// on the listed port, not a bug in the handshake itself).
func (t *telegramInfo) Addresses(dc DC) []string {
	t.mu.RLock()
	defer t.mu.RUnlock()

	var out []string
	if addr := chooseAddress(t.v6Addresses, dc, t.v6Default); addr != "" {
		out = append(out, addr)
	}
	if addr := chooseAddress(t.v4Addresses, dc, t.v4Default); addr != "" {
		out = append(out, addr)
	}

	return out
}

func chooseAddress(addrs map[DC][]string, dc, defaultDC DC) string {
	if addrs == nil {
		return ""
	}
	list, ok := addrs[dc]
	if !ok {
		list = addrs[defaultDC]
	}
	if len(list) == 0 {
		return ""
	}
	// A fixed pick (not random) keeps behavior deterministic and easy to
	// reason about for this port; Telegram lists multiple addresses purely
	// for redundancy, any one of them is a valid choice.
	return list[0]
}

func fetchSecret(ctx context.Context) ([]byte, error) {
	body, err := httpGet(ctx, secretURL)
	if err != nil {
		return nil, err
	}
	defer body.Close()

	buf, err := io.ReadAll(body)
	if err != nil {
		return nil, fmt.Errorf("middleproxy: cannot read proxy secret response: %w", err)
	}
	if len(buf) == 0 {
		return nil, fmt.Errorf("middleproxy: empty proxy secret response")
	}

	return buf, nil
}

func fetchAddresses(ctx context.Context, url string) (map[DC][]string, DC, error) {
	body, err := httpGet(ctx, url)
	if err != nil {
		return nil, 0, err
	}
	defer body.Close()

	data := map[DC][]string{}
	defaultDC := DefaultDC

	scanner := bufio.NewScanner(body)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		switch {
		case strings.HasPrefix(line, "#"):
			continue
		case strings.HasPrefix(line, "proxy_for"):
			addr, dc, err := parseProxyFor(line)
			if err != nil {
				continue
			}
			data[dc] = append(data[dc], addr)
		case strings.HasPrefix(line, "default"):
			if dc, err := parseDefault(line); err == nil {
				defaultDC = dc
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, 0, fmt.Errorf("middleproxy: cannot parse proxy config: %w", err)
	}

	return data, defaultDC, nil
}

func parseProxyFor(line string) (string, DC, error) {
	chunks := strings.SplitN(line, " ", 3)
	if len(chunks) != 3 || chunks[0] != "proxy_for" {
		return "", 0, fmt.Errorf("middleproxy: bad proxy_for line %q", line)
	}

	dc, err := strconv.ParseInt(chunks[1], 10, 16)
	if err != nil {
		return "", 0, fmt.Errorf("middleproxy: bad dc in proxy_for line %q: %w", line, err)
	}

	addr := strings.TrimRight(strings.TrimSpace(chunks[2]), ";")
	if _, _, err := net.SplitHostPort(addr); err != nil {
		return "", 0, fmt.Errorf("middleproxy: bad address in proxy_for line %q: %w", line, err)
	}

	return addr, DC(dc), nil
}

func parseDefault(line string) (DC, error) {
	chunks := strings.SplitN(line, " ", 2)
	if len(chunks) != 2 || chunks[0] != "default" {
		return 0, fmt.Errorf("middleproxy: bad default line %q", line)
	}

	dcStr := strings.TrimRight(strings.TrimSpace(chunks[1]), ";")
	dc, err := strconv.ParseInt(dcStr, 10, 16)
	if err != nil {
		return 0, fmt.Errorf("middleproxy: bad dc in default line %q: %w", line, err)
	}

	return DC(dc), nil
}

func httpGet(ctx context.Context, url string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("middleproxy: cannot build request for %s: %w", url, err)
	}
	req.Header.Set("User-Agent", "pasarguard-node-mtproto-middleproxy")

	resp, err := tgHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("middleproxy: cannot fetch %s: %w", url, err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("middleproxy: %s returned status %d", url, resp.StatusCode)
	}

	return resp.Body, nil
}

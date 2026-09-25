package xray

import (
	"context"
	"errors"
	"fmt"
	"log"
	"slices"
	"strings"
	"time"

	"github.com/pasarguard/node/backend/xray/api"
	"github.com/pasarguard/node/common"
)

func setupUserAccount(user *common.User) (api.ProxySettings, error) {
	settings := api.ProxySettings{}
	if user.GetProxies().GetVmess() != nil {
		if vmessAccount, err := api.NewVmessAccount(user); err == nil {
			settings.Vmess = vmessAccount
		}
	}

	if user.GetProxies().GetVless() != nil {
		if vlessAccount, err := api.NewVlessAccount(user); err == nil {
			settings.Vless = vlessAccount
		}
	}

	if user.GetProxies().GetTrojan() != nil {
		settings.Trojan = api.NewTrojanAccount(user)
	}

	if user.GetProxies().GetShadowsocks() != nil {
		settings.Shadowsocks = api.NewShadowsocksTcpAccount(user)
		settings.Shadowsocks2022 = api.NewShadowsocksAccount(user)
	}

	if hasHysteriaAuth(user) {
		settings.Hysteria = api.NewHysteriaAccount(user)
	}

	return settings, nil
}

func hasHysteriaAuth(user *common.User) bool {
	return user.GetProxies().GetHysteria().GetAuth() != ""
}

func inboundFlow(inbound *Inbound) string {
	if inbound == nil || inbound.Settings == nil {
		return ""
	}
	flow, _ := inbound.Settings["flow"].(string)
	return flow
}

func accountForAPI(inbound *Inbound, account api.Account) api.Account {
	vlessAccount, ok := account.(*api.VlessAccount)
	if !ok {
		return account
	}

	overrideFlow := inboundFlow(inbound)
	if overrideFlow == "" {
		return account
	}

	copy := *vlessAccount
	copy.Flow = overrideFlow
	return &copy
}

func checkShadowsocks2022(method string, account api.ShadowsocksAccount) api.ShadowsocksAccount {
	account.Password = common.EnsureBase64Password(account.Password, method)

	return account
}

func isActiveInbound(inbound *Inbound, inbounds []string, settings api.ProxySettings) (api.Account, bool) {
	if slices.Contains(inbounds, inbound.Tag) {
		switch inbound.Protocol {
		case Vless:
			if settings.Vless == nil {
				return nil, false
			}
			return settings.Vless, true

		case Vmess:
			if settings.Vmess == nil {
				return nil, false
			}
			return settings.Vmess, true

		case Trojan:
			if settings.Trojan == nil {
				return nil, false
			}
			return settings.Trojan, true

		case Shadowsocks:
			method, ok := inbound.Settings["method"].(string)
			if ok && strings.HasPrefix(method, "2022-blake3") {
				if settings.Shadowsocks2022 == nil {
					return nil, false
				}
				account := checkShadowsocks2022(method, *settings.Shadowsocks2022)

				return &account, true
			}
			if settings.Shadowsocks == nil {
				return nil, false
			}
			return settings.Shadowsocks, true

		case Hysteria:
			if settings.Hysteria == nil {
				return nil, false
			}
			return settings.Hysteria, true
		}
	}
	return nil, false
}

func revokeInboundUser(ctx context.Context, handler *api.XrayHandler, tag, email string) error {
	err := handler.RevokeInboundUser(ctx, tag, email)
	if errors.Is(err, api.ErrRevokeUnsupported) {
		err = handler.RemoveInboundUser(ctx, tag, email)
		if err == nil || api.IsAbsentUser(err, email) {
			return nil
		}
		return fmt.Errorf("remove %s from %s: %w", email, tag, err)
	}
	if err == nil || api.IsAbsentUser(err, email) {
		return nil
	}
	_ = handler.RemoveInboundUser(ctx, tag, email)
	return fmt.Errorf("revoke %s on %s: %w", email, tag, err)
}

func (x *Xray) lockSync(ctx context.Context) func() {
	x.syncMu.Lock()
	if x.syncEntered != nil {
		x.syncEntered(ctx)
	}
	return x.syncMu.Unlock
}

func pushAccount(ctx context.Context, handler *api.XrayHandler, inbound *Inbound, account api.Account, step coreStep) error {
	if step == coreUnchanged {
		return nil
	}

	email := account.GetEmail()
	var revokeErr error
	if step == coreReplace {
		revokeErr = revokeInboundUser(ctx, handler, inbound.Tag, email)
	}
	if err := handler.AddInboundUser(ctx, inbound.Tag, accountForAPI(inbound, account)); err != nil {
		inbound.dropAccount(email)
		return errors.Join(revokeErr, err)
	}
	if revokeErr != nil {
		inbound.markUnsettled(email)
		return revokeErr
	}
	inbound.settle(email)
	return nil
}

func revokeLeavingUser(ctx context.Context, handler *api.XrayHandler, inbound *Inbound, email string) error {
	if err := revokeInboundUser(ctx, handler, inbound.Tag, email); err != nil {
		inbound.markUnsettled(email)
		return err
	}
	inbound.settle(email)
	return nil
}

func (x *Xray) SyncUser(ctx context.Context, user *common.User) error {
	unlock := x.lockSync(ctx)
	defer unlock()

	proxySetting, err := setupUserAccount(user)
	if err != nil {
		return err
	}

	handler := x.handler
	inbounds := x.config.InboundConfigs

	var errMessage strings.Builder

	userInbounds := user.GetInbounds()

	for _, inbound := range inbounds {
		if inbound.exclude {
			continue
		}

		var err error
		account, isActive := isActiveInbound(inbound, userInbounds, proxySetting)
		switch {
		case isActive:
			err = pushAccount(ctx, handler, inbound, account, inbound.stageAccount(account))
		case inbound.stageRemoval(user.GetEmail()):
			err = revokeLeavingUser(ctx, handler, inbound, user.GetEmail())
		}
		if err != nil {
			log.Println(err)
			errMessage.WriteString("\n" + err.Error())
		}
	}

	if errMessage.String() != "" {
		return errors.New("failed to add user:" + errMessage.String())
	}
	return nil
}

func (x *Xray) SyncUsers(ctx context.Context, users []*common.User) error {
	unlock := x.lockSync(ctx)
	defer unlock()

	candidate, err := x.config.Clone()
	if err != nil {
		return err
	}

	candidate.syncUsers(users)
	return x.applyConfigWithRestart(ctx, candidate)
}

func (x *Xray) applyConfigWithRestart(ctx context.Context, candidate *Config) error {
	previous := x.config

	if err := x.restartCoreWithConfig(candidate); err != nil {
		if restoreErr := x.restorePreviousConfig(previous); restoreErr != nil {
			return fmt.Errorf("%w; failed to restore previous xray config: %v", err, restoreErr)
		}
		return err
	}

	if err := x.checkXrayStatus(ctx); err != nil {
		if restoreErr := x.restorePreviousConfig(previous); restoreErr != nil {
			return fmt.Errorf("%w; failed to restore previous xray config: %v", err, restoreErr)
		}
		return err
	}

	x.setConfig(candidate)
	return nil
}

func (x *Xray) restorePreviousConfig(previous *Config) error {
	if previous == nil {
		return errors.New("previous xray config is nil")
	}

	log.Println("restoring previous xray config after failed restart")
	if err := x.restartCoreWithConfig(previous); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := x.checkXrayStatus(ctx); err != nil {
		return err
	}

	x.setConfig(previous)
	return nil
}

func (x *Xray) UpdateUsers(ctx context.Context, users []*common.User) error {
	unlock := x.lockSync(ctx)
	defer unlock()

	handler := x.handler
	inboundByTag, updates := x.config.buildInboundUpdates(users)
	var errMessage strings.Builder

	for tag, update := range updates {
		removeEmails := make([]string, 0, len(update.removeEmailSet))
		for email := range update.removeEmailSet {
			removeEmails = append(removeEmails, email)
		}

		inbound := inboundByTag[tag]
		steps, leaving := inbound.stageUpdate(update.accounts, removeEmails)

		for _, email := range leaving {
			if err := revokeLeavingUser(ctx, handler, inbound, email); err != nil {
				log.Println(err)
				errMessage.WriteString("\n" + err.Error())
			}
		}

		for index, account := range update.accounts {
			if err := pushAccount(ctx, handler, inbound, account, steps[index]); err != nil {
				log.Println(err)
				errMessage.WriteString("\n" + err.Error())
			}
		}
	}

	if errMessage.String() != "" {
		return errors.New("failed to update users:" + errMessage.String())
	}

	return nil
}

func (x *Xray) UpdateUsersAndRestart(ctx context.Context, users []*common.User) error {
	unlock := x.lockSync(ctx)
	defer unlock()

	candidate, err := x.config.Clone()
	if err != nil {
		return err
	}

	candidate.updateUsers(users)
	return x.applyConfigWithRestart(ctx, candidate)
}

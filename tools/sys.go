package tools

import (
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/mem"
	"github.com/shirou/gopsutil/v4/net"

	"github.com/pasarguard/node/common"
)

func GetSystemStats() (*common.SystemStatsResponse, error) {
	stats := &common.SystemStatsResponse{}

	vm, err := mem.VirtualMemory()
	if err != nil {
		return stats, err
	}
	stats.MemTotal = vm.Total
	stats.MemUsed = vm.Used

	cores, err := cpu.Counts(true)
	if err != nil {
		return stats, err
	}
	stats.CpuCores = uint64(cores)

	percentages, err := cpu.Percent(time.Second, false)
	if err != nil {
		return stats, err
	}
	if len(percentages) > 0 {
		stats.CpuUsage = percentages[0]
	}

	incomingSpeed, outgoingSpeed, err := getBandwidthSpeed()
	if err != nil {
		return stats, err
	}
	stats.IncomingBandwidthSpeed = incomingSpeed
	stats.OutgoingBandwidthSpeed = outgoingSpeed

	return stats, nil
}

// getBandwidthSpeed returns the aggregate incoming (rx) and outgoing (tx)
// bandwidth in bytes per second, sampled over a 1‑second interval.
// Loopback interface (lo) is excluded from the calculation.
func getBandwidthSpeed() (uint64, uint64, error) {
	// 1) First snapshot with timestamp
	start := time.Now()
	first, err := net.IOCounters(true)
	if err != nil {
		return 0, 0, err
	}

	// 2) Wait one second
	time.Sleep(1 * time.Second)

	// 3) Second snapshot with timestamp
	second, err := net.IOCounters(true)
	if err != nil {
		return 0, 0, err
	}
	end := time.Now()

	// 4) Calculate actual elapsed time (not assumed 1 second)
	actualDuration := end.Sub(start).Seconds()
	if actualDuration == 0 {
		return 0, 0, nil // avoid division by zero
	}

	// 5) Build a map from interface name → first snapshot
	// Skip loopback interface
	prev := make(map[string]net.IOCountersStat, len(first))
	for _, c := range first {
		// Skip loopback interface
		if c.Name == "lo" {
			continue
		}
		prev[c.Name] = c
	}

	// 6) Compute deltas and sum across all interfaces
	// Skip loopback interface
	var totalRxBytes, totalTxBytes uint64
	for _, c := range second {
		// Skip loopback interface
		if c.Name == "lo" {
			continue
		}
		if p, ok := prev[c.Name]; ok {
			totalRxBytes += c.BytesRecv - p.BytesRecv
			totalTxBytes += c.BytesSent - p.BytesSent
		}
	}

	// 7) Convert bytes to bytes per second using ACTUAL measured time
	rxPerSecond := uint64(float64(totalRxBytes) / actualDuration)
	txPerSecond := uint64(float64(totalTxBytes) / actualDuration)

	// 8) Return the calculated rates (bytes per second)
	return rxPerSecond, txPerSecond, nil
}

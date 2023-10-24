package pqos

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/yuanqijing/resctrl/pkg/utils"
	"k8s.io/klog/v2"
)

type Monitor struct {
	interval int // 100ms per unit
	pid      pid
}

func NewMonitor() *Monitor {
	return &Monitor{interval: 1}
}

func (p *Monitor) Socket(socket ...int) *Monitor { panic("not support") }
func (p *Monitor) Core(core ...int) *Monitor     { panic("not support") }
func (p *Monitor) Pid(pid ...int) *Monitor       { p.pid = append(p.pid, pid...); return p }

type pid []int

// arg constructs and returns a formatted string argument for monitoring
// specific PIDs using the pqos tool. The resulting argument has the format
// "--mon-pid=all:[PIDs];mbt:[PIDs]" where [PIDs] is a comma-separated list
// of process IDs.
func (p *pid) arg() string {
	out := "--mon-pid=%s"
	template := "all:[%s];mbt:[%s]"
	var pids string
	for _, v := range *p {
		pids += fmt.Sprintf("%d,", v)
	}
	if len(pids) > 0 {
		pids = strings.TrimSuffix(pids, ",")
		return fmt.Sprintf(out, fmt.Sprintf(template, pids, pids))
	}
	return ""
}

// kill attempts to gracefully shut down the provided pQos command.
// First, it tries to send an interrupt signal to the command's process.
// If the process is still running after a specified timeout (2 seconds),
// it forcefully kills the process using SIGTERM.
func kill(pQos *exec.Cmd) error {
	if pQos.Process != nil {
		// try to send interrupt signal
		_ = pQos.Process.Signal(os.Interrupt)

		// wait and contantly check if pqos is still running
		ctx, cancel := context.WithTimeout(context.Background(), time.Second*2)
		defer cancel()
		for {
			if err := pQos.Process.Signal(syscall.Signal(0)); errors.Is(err, os.ErrProcessDone) {
				return nil
			} else if ctx.Err() != nil {
				break
			}
		}

		// if pqos is still running after some period, try to kill it
		// this will send SIGTERM to pqos, and leave garbage in `/sys/fs/resctrl/mon_groups`
		// fixed in https://github.com/intel/intel-cmt-cat/issues/197
		err := pQos.Process.Kill()
		if err != nil {
			return fmt.Errorf("failed to shut down pqos: %w", err)
		}
	}

	return nil
}

func (p *Monitor) Exec(ch <-chan struct{}, fn func(in io.ReadCloser)) error {
	args := []string{
		"--mon-reset", // monitoring reset, claim all RMID's
		"--iface-os",  // set the library interface to use the kernel implementation.
		"--mon-file-type=csv",
		fmt.Sprintf("--mon-interval=%d", p.interval*10),
	}
	args = append(args, p.pid.arg())

	cmd := exec.Command("pqos", args...)
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	go fn(out)

	utils.Defer(ch, func() {
		if err := kill(cmd); err != nil {
			klog.Errorf("failed to kill pqos: %s", err)
		}
	})

	if err := cmd.Start(); err != nil {
		return err
	}

	if err := cmd.Wait(); err != nil {
		return err
	}

	klog.Infof("pqos exec completed")
	return nil
}

type Metrics struct {
	Pid        []int   `json:"Pid"`
	Cores      []int   `json:"cores"`
	IPC        float64 `json:"ipc"`
	LLC_Misses int     `json:"llc_misses"`
	LLC        int     `json:"llc"` // llc usage[KB]
	MBL        float64 `json:"mbl"` // memory bandwidth local[MB/s]
	MBR        float64 `json:"mbr"` // memory bandwidth remote[MB/s]
}

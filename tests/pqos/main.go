package main

import (
	"bufio"
	"github.com/yuanqijing/resctrl/pkg/pqos"
	"io"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/manager/signals"
)

func main() {
	stopCtx := signals.SetupSignalHandler()

	if err := pqos.NewMonitor().Pid(1957712).Exec(stopCtx.Done(), func(in io.ReadCloser) {
		scanner := bufio.NewScanner(in)
		/*
			Omit first 4 lines :
			"NOTE:  Mixed use of MSR and kernel interfaces to manage
					CAT or CMT & MBM may lead to unexpected behavior.\n"
			CMT/MBM reset successful
			"Time,Core,IPC,LLC Misses,LLC[KB],MBL[MB/s],MBR[MB/s],MBT[MB/s]\n"
		*/
		klog.Info("pqos exec completed")
		for i := 0; i < 4; i++ {
			scanner.Scan()
		}

		for scanner.Scan() {
			out := scanner.Text()
			klog.Info(out)
		}
	}); err != nil {
		panic(err)
	}
}

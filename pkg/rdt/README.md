

```bash
[root@hs-10-20-30-227 resctrl]# pqos -d
NOTE:  Mixed use of MSR and kernel interfaces to manage
       CAT or CMT & MBM may lead to unexpected behavior.
Hardware capabilities
    Monitoring
        Cache Monitoring Technology (CMT) events:
            LLC Occupancy (LLC)
        Memory Bandwidth Monitoring (MBM) events:
            Total Memory Bandwidth (TMEM)
            Local Memory Bandwidth (LMEM)
            Remote Memory Bandwidth (RMEM) (calculated)
        PMU events:
            Instructions/Clock (IPC)
            LLC misses
    Allocation
        Cache Allocation Technology (CAT)
            L3 CAT
                CDP: disabled
                Num COS: 15
            L2 CAT
                CDP: disabled
                Num COS: 8
        Memory Bandwidth Allocation (MBA)
            Num COS: 15
```

the `pqos` utility's `-d` (or `--display`)  shows the capabilities of the rdt.
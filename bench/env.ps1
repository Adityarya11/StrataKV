# Prints the facts a benchmark result needs. Read-only; changes nothing.

$os  = Get-CimInstance Win32_OperatingSystem
$cs  = Get-CimInstance Win32_ComputerSystem
$cpu = Get-CimInstance Win32_Processor | Select-Object -First 1

"date                 : " + (Get-Date -Format 'yyyy-MM-dd HH:mm')
"machine              : " + $cs.Manufacturer + " " + $cs.Model
"cpu                  : " + $cpu.Name.Trim() + " (" + $cpu.NumberOfCores + " cores / " + $cpu.NumberOfLogicalProcessors + " threads)"
"ram total (GB)       : " + [math]::Round($cs.TotalPhysicalMemory / 1GB, 1)
"ram free at start (GB): " + [math]::Round($os.FreePhysicalMemory / 1MB, 1)
"os                   : " + $os.Caption + " build " + $os.BuildNumber
"power plan           : " + ((powercfg /getactivescheme) -replace '^.*: ', '')

$b = Get-CimInstance Win32_Battery -ErrorAction SilentlyContinue
if ($b) { "battery              : {0}% (status {1}; 2/3/6-9/11 = on AC)" -f $b.EstimatedChargeRemaining, $b.BatteryStatus }
else    { "battery              : none (desktop)" }

Get-PhysicalDisk | ForEach-Object {
    "disk                 : {0} | {1} | {2} | {3} GB" -f $_.FriendlyName, $_.MediaType, $_.BusType, [math]::Round($_.Size / 1GB)
}

try {
    $mp = Get-MpComputerStatus -ErrorAction Stop
    "defender real-time   : " + $mp.RealTimeProtectionEnabled
} catch { "defender real-time   : unreadable" }

try {
    $ex = (Get-MpPreference -ErrorAction Stop).ExclusionPath
    "defender exclusions  : " + $(if ($ex) { $ex -join '; ' } else { 'none' })
} catch { "defender exclusions  : unreadable (needs administrator)" }

# The system timer resolution, which other programs (browsers) can lower to 1 ms.
# It limits time.Now() on Windows; the workload driver uses the performance
# counter instead, but testing.B and anything else using time.Now() see this.
try {
    Add-Type -TypeDefinition @"
using System.Runtime.InteropServices;
public static class NtTimer {
    [DllImport("ntdll.dll")] public static extern int NtQueryTimerResolution(out uint min, out uint max, out uint cur);
}
"@ -ErrorAction Stop
    [uint32]$mn = 0; [uint32]$mx = 0; [uint32]$cu = 0
    [void][NtTimer]::NtQueryTimerResolution([ref]$mn, [ref]$mx, [ref]$cu)
    "system timer         : {0} ms current (best available {1} ms)" -f ($cu / 10000), ($mx / 10000)
} catch { "system timer         : unreadable" }

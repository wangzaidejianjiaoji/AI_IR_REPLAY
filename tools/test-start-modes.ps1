# Isolated launcher tests: fake executable and credentials, no network/reviews.
$ErrorActionPreference = 'Stop'
$root = Split-Path $PSScriptRoot -Parent
$fixture = Join-Path ([IO.Path]::GetTempPath()) ('review launcher ' + [guid]::NewGuid())
$previousLocal = $env:LOCALAPPDATA
$previousArgs = $env:TEST_ARGS_FILE
$previousExit = $env:TEST_EXIT_CODE
try {
    New-Item -ItemType Directory -Force -Path "$fixture/bin", "$fixture/configs", "$fixture/local/AI_IR_REPLAY" | Out-Null
    Copy-Item "$root/start-persistent.ps1" $fixture
    Set-Content "$fixture/local/AI_IR_REPLAY/runtime-env.ps1" '$env:KIMI_API_KEY="mock"; $env:CODEUP_TOKEN="mock"; $env:CODEUP_ORG_ID="mock"'
    Set-Content "$fixture/configs/config-gvs.yaml" ''
    Set-Content "$fixture/configs/config-gvs-infer.yaml" ''
    Set-Content "$fixture/run-persistent.ps1" 'param($Instance); Set-Content (Join-Path $PSScriptRoot ("scheduled-" + $Instance + ".txt")) $Instance'
    $source = @'
package main
import("encoding/json"; "os"; "strconv")
func main(){b,_:=json.Marshal(os.Args[1:]);os.WriteFile(os.Getenv("TEST_ARGS_FILE"),b,0600);n,_:=strconv.Atoi(os.Getenv("TEST_EXIT_CODE"));os.Exit(n)}
'@
    Set-Content "$fixture/fake.go" $source
    & "$root/.gotmp/go1.23.12/go/bin/go.exe" build -o "$fixture/bin/gvs-pr-agent.exe" "$fixture/fake.go"
    if ($LASTEXITCODE -ne 0) { throw 'fake build failed' }
    $env:LOCALAPPDATA = "$fixture/local"
    $env:TEST_ARGS_FILE = "$fixture/args.json"
    $env:TEST_EXIT_CODE = '42'
    $shell = "$env:SystemRoot/System32/WindowsPowerShell/v1.0/powershell.exe"
    function Invoke-Launcher([string[]]$Options) {
        $savedPreference = $ErrorActionPreference
        try {
            $ErrorActionPreference = 'Continue'
            & $shell -NoProfile -ExecutionPolicy Bypass -File "$fixture/start-persistent.ps1" @Options *> $null
            return $LASTEXITCODE
        } finally { $ErrorActionPreference = $savedPreference }
    }
    # stop/status must route before credentials are read and preserve exit codes.
    Move-Item "$fixture/local/AI_IR_REPLAY/runtime-env.ps1" "$fixture/credentials.saved"
    foreach ($action in @('stop', 'status', 'restart')) {
        if ((Invoke-Launcher @('-Action', $action, '-Instance', 'gvs')) -ne 42) { throw "action routing failed: $action" }
        $actual = Get-Content "$fixture/args.json" -Raw | ConvertFrom-Json
        $want = @('-action', $action, '-instance', 'gvs')
        if (($actual -join '|') -ne ($want -join '|')) { throw "action arguments mismatch: $actual" }
    }
    Move-Item "$fixture/credentials.saved" "$fixture/local/AI_IR_REPLAY/runtime-env.ps1"
    $status = Invoke-Launcher @('-Mode','manual','-Instance','gvs','-MR','27','-DryRun','-CheckpointDir','data/checkpoints with spaces')
    if ($status -ne 42) { throw "manual exit code: $status (expected 42)" }
    $actual = Get-Content "$fixture/args.json" -Raw | ConvertFrom-Json
    $want = @('-config','configs/config-gvs.yaml','-repo','lizheng/lzvsg/gvs','-mr','27','-checkpoint-dir','data/checkpoints with spaces','-dry-run')
    if (($actual -join '|') -ne ($want -join '|')) { throw "manual argument mismatch: $actual" }
    if (Test-Path "$fixture/scheduled-*.txt") { throw 'manual mode started a supervisor' }
    $env:TEST_EXIT_CODE = '0'
    if ((Invoke-Launcher @('-Mode','manual','-Instance','gvs-infer','-MR','81')) -ne 0) { throw 'manual success exit code lost' }
    $actual = Get-Content "$fixture/args.json" -Raw | ConvertFrom-Json
    $want = @('-config','configs/config-gvs-infer.yaml','-repo','lizheng/lzvsg/gvs_infer','-mr','81','-checkpoint-dir','data/checkpoints/gvs-infer')
    if (($actual -join '|') -ne ($want -join '|')) { throw "infer argument mismatch: $actual" }
    foreach ($options in @(
        @('-Mode','manual','-MR','27'),
        @('-Mode','manual','-Instance','gvs'),
        @('-Mode','manual','-Instance','gvs','-MR','-1'),
        @('-Mode','scheduled','-MR','27'),
        @('-Mode','scheduled','-DryRun'),
        @('-Mode','scheduled','-Repo','other/repo'),
        @('-Mode','scheduled','-CheckpointDir','data/cache'),
        @('-UnknownOption','manual'),
        @('-Action','stop','-Mode','manual','-Instance','gvs','-MR','27'),
        @('-Action','status','-Repo','other/repo')
    )) {
        Remove-Item "$fixture/args.json" -ErrorAction SilentlyContinue
        if ((Invoke-Launcher $options) -eq 0) { throw "invalid selection accepted: $options" }
        if (Test-Path "$fixture/args.json") { throw 'invalid selection invoked agent' }
    }
    $lockPath = "$fixture/local/AI_IR_REPLAY/gvs.supervisor.lock"
    $lock = [IO.File]::Open($lockPath, 'OpenOrCreate', 'ReadWrite', 'None')
    try {
        if ((Invoke-Launcher @('-Mode','manual','-Instance','gvs','-MR','27')) -eq 0) { throw 'active supervisor allowed manual launch' }
        if (Test-Path "$fixture/args.json") { throw 'locked manual selection invoked agent' }
    } finally { $lock.Dispose() }
    if ((Invoke-Launcher @()) -ne 0) { throw 'default scheduled launch failed' }
    for ($i=0; $i -lt 50; $i++) {
        if ((Test-Path "$fixture/scheduled-gvs.txt") -and (Test-Path "$fixture/scheduled-gvs-infer.txt")) { break }
        Start-Sleep -Milliseconds 100
    }
    if (-not ((Test-Path "$fixture/scheduled-gvs.txt") -and (Test-Path "$fixture/scheduled-gvs-infer.txt"))) { throw 'default no longer starts both scheduled instances' }
    Write-Output 'PASS: lifecycle routing without credentials, manual arguments, exit codes, invalid input, lock exclusion, default scheduled launch.'
} finally {
    $env:LOCALAPPDATA = $previousLocal
    $env:TEST_ARGS_FILE = $previousArgs
    $env:TEST_EXIT_CODE = $previousExit
    # Exact, absolute directory created above; never delete workspace contents.
    if ($fixture.StartsWith([IO.Path]::GetTempPath()) -and (Test-Path -LiteralPath $fixture)) {
        for ($attempt=0; $attempt -lt 20; $attempt++) {
            try { Remove-Item -LiteralPath $fixture -Recurse -Force; break }
            catch { if ($attempt -eq 19) { throw }; Start-Sleep -Milliseconds 200 }
        }
    }
}

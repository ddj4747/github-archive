param(
    [string]$Config = 'config.yaml',
    [string]$TokenEnv = 'GITHUB_TOKEN'
)

$ErrorActionPreference = 'Stop'
if ($TokenEnv -notmatch '^[A-Za-z_][A-Za-z0-9_]*$') { throw 'Invalid token environment variable name.' }
$originalToken = [Environment]::GetEnvironmentVariable($TokenEnv, 'Process')
$originalPrompt = [Environment]::GetEnvironmentVariable('GIT_TERMINAL_PROMPT', 'Process')
$originalInteractive = [Environment]::GetEnvironmentVariable('GCM_INTERACTIVE', 'Process')
try {
    if ([string]::IsNullOrWhiteSpace($originalToken) -and (Get-Command git -ErrorAction SilentlyContinue)) {
        $env:GIT_TERMINAL_PROMPT = '0'
        $env:GCM_INTERACTIVE = 'Never'
        $credentialInput = "protocol=https`nhost=github.com`n`n"
        $ErrorActionPreference = 'Continue'
        try { $credentialLines = $credentialInput | git credential fill 2>$null }
        finally { $ErrorActionPreference = 'Stop' }
        $passwordLine = $credentialLines | Where-Object { $_ -like 'password=*' } | Select-Object -First 1
        if ($passwordLine) {
            [Environment]::SetEnvironmentVariable($TokenEnv, $passwordLine.Substring(9), 'Process')
        }
    }
    $goExe = Join-Path $env:ProgramFiles 'Go\bin\go.exe'
    if (-not (Test-Path -LiteralPath $goExe)) { throw 'Go was not found at the expected location.' }
    & $goExe run -mod=readonly ./cmd/github-archive -config $Config
    if ($LASTEXITCODE -ne 0) { throw 'The archive process exited with an error.' }
} finally {
    [Environment]::SetEnvironmentVariable($TokenEnv, $originalToken, 'Process')
    [Environment]::SetEnvironmentVariable('GIT_TERMINAL_PROMPT', $originalPrompt, 'Process')
    [Environment]::SetEnvironmentVariable('GCM_INTERACTIVE', $originalInteractive, 'Process')
}

[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [ValidatePattern('^v?[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z]+(?:[.-][0-9A-Za-z]+)*)?$')]
    [string]$Version,
    [string]$Go = 'go'
)

$ErrorActionPreference = 'Stop'
$repoRoot = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
$distRoot = Join-Path $repoRoot 'dist'
$stageRoot = Join-Path $distRoot ('stage-' + [guid]::NewGuid().ToString('N'))
$archiveName = 'amp-mcp-windows-x64.zip'
$previousGoos = $env:GOOS
$previousGoarch = $env:GOARCH
$previousCgo = $env:CGO_ENABLED

function Invoke-Go {
    param([string[]]$GoArgs)
    & $Go @GoArgs
    if ($LASTEXITCODE -ne 0) { throw "Go failed: $($GoArgs[0])" }
}

Push-Location $repoRoot
try {
    $env:GOOS = 'windows'
    $env:GOARCH = 'amd64'
    $env:CGO_ENABLED = '0'
    New-Item -ItemType Directory -Path (Join-Path $stageRoot 'docs') -Force | Out-Null
    $executable = Join-Path $stageRoot 'amp-mcp.exe'
    Invoke-Go -GoArgs @('build', '-trimpath', '-ldflags', "-s -w -X main.version=$Version", '-o', $executable, '.')
    $actualVersion = & $executable version
    if ($LASTEXITCODE -ne 0 -or $actualVersion -ne "amp-mcp $Version") { throw 'Packaged executable version mismatch.' }

    # Explicit public payload. Never recurse over the checkout or local state.
    foreach ($relative in @('README.md', 'LICENSE', 'docs/setup.md', 'docs/tools.md', 'docs/troubleshooting.md')) {
        Copy-Item -LiteralPath (Join-Path $repoRoot $relative) -Destination (Join-Path $stageRoot $relative)
    }

    # Include dependency copyright/license text with the distributed binary.
    $modules = Invoke-Go -GoArgs @('list', '-deps', '-f', '{{with .Module}}{{.Path}}|{{.Version}}|{{.Dir}}{{end}}', '.') |
        Where-Object { $_ } | Sort-Object -Unique
    $notices = [Text.StringBuilder]::new()
    [void]$notices.AppendLine('AMP-MCP third-party notices')
    [void]$notices.AppendLine('The following components retain their respective licenses.')
    foreach ($moduleLine in $modules) {
        $parts = $moduleLine -split '\|', 3
        if ($parts[0] -eq 'amp-mcp') { continue }
        $licensePath = $null
        foreach ($candidate in @('LICENSE', 'LICENSE.txt', 'LICENSE.md', 'COPYING')) {
            $candidatePath = Join-Path $parts[2] $candidate
            if (Test-Path -LiteralPath $candidatePath -PathType Leaf) { $licensePath = $candidatePath; break }
        }
        if (-not $licensePath) { throw "Missing license text for $($parts[0])." }
        [void]$notices.AppendLine("`n--- $($parts[0]) $($parts[1]) ---`n")
        [void]$notices.AppendLine([IO.File]::ReadAllText($licensePath))
        $noticePath = Join-Path $parts[2] 'NOTICE'
        if (Test-Path -LiteralPath $noticePath -PathType Leaf) { [void]$notices.AppendLine([IO.File]::ReadAllText($noticePath)) }
    }
    $goRoot = Invoke-Go -GoArgs @('env', 'GOROOT')
    [void]$notices.AppendLine("`n--- Go runtime ---`n")
    [void]$notices.AppendLine([IO.File]::ReadAllText((Join-Path $goRoot 'LICENSE')))
    $goPatents = Join-Path $goRoot 'PATENTS'
    if (Test-Path -LiteralPath $goPatents -PathType Leaf) { [void]$notices.AppendLine([IO.File]::ReadAllText($goPatents)) }
    [IO.File]::WriteAllText((Join-Path $stageRoot 'THIRD_PARTY_NOTICES.txt'), $notices.ToString(), [Text.UTF8Encoding]::new($false))

    $archivePath = Join-Path $distRoot $archiveName
    Compress-Archive -Path (Join-Path $stageRoot '*') -DestinationPath $archivePath -Force
    $hash = (Get-FileHash -LiteralPath $archivePath -Algorithm SHA256).Hash.ToLowerInvariant()
    [IO.File]::WriteAllText((Join-Path $distRoot 'SHA256SUMS'), "$hash  $archiveName`n", [Text.UTF8Encoding]::new($false))
    Write-Output "Created $archivePath ($Version)"
} finally {
    $env:GOOS = $previousGoos
    $env:GOARCH = $previousGoarch
    $env:CGO_ENABLED = $previousCgo
    Pop-Location
    # Remove only this invocation's staging directory, after checking its scope.
    $resolvedStage = [IO.Path]::GetFullPath($stageRoot)
    $expectedParent = [IO.Path]::GetFullPath($distRoot) + [IO.Path]::DirectorySeparatorChar
    if (-not $resolvedStage.StartsWith($expectedParent, [StringComparison]::OrdinalIgnoreCase)) { throw 'Unsafe staging path.' }
    if (Test-Path -LiteralPath $resolvedStage) { Remove-Item -LiteralPath $resolvedStage -Recurse -Force }
}

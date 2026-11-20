# Rebuild and publish to the existing local Docker/Vite development setup.
# Run from PowerShell: .\scripts\publish-dev.ps1
$ErrorActionPreference = 'Stop'

function Invoke-Docker {
    param([string[]]$DockerArgs)
    & docker @DockerArgs
    if ($LASTEXITCODE -ne 0) {
        throw "Docker command failed: $($DockerArgs[0])"
    }
}

$vite = '3x-ui-local-vite'
$backend = '3x-ui-local-backend'
$image = '3x-ui-local-published'
$previous = "$backend-rollback-$(Get-Date -Format yyyyMMdd-HHmmss-fff)"
$origin = 'http://127.0.0.1:5174'
$project = Split-Path -Parent $PSScriptRoot

Push-Location -LiteralPath $project
try {
    Invoke-Docker @('container', 'inspect', '--format', '{{.State.Status}}', $vite)
    Invoke-Docker @('container', 'inspect', '--format', '{{.State.Status}}', $backend)
    Invoke-Docker @('image', 'inspect', '--format', '{{.Id}}', '3x-ui-local-dev')

    # Reuse the local runtime image's Xray binaries and build the panel
    # from current source. Keep go.mod, go.sum, and the production Dockerfile intact.
    $source = Get-Content -LiteralPath Dockerfile -Raw
    $pattern = '(?m)^RUN go build -ldflags[^\r\n]*$'
    $build = [regex]::Match($source, $pattern)
    if (!$build.Success) { throw 'Cannot locate the Go build instruction in Dockerfile.' }

    $dockerfile = $source.Substring(0, $build.Index)
    $dockerfile += "RUN apk add --no-cache git`n"
    $dockerfile += 'RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build GONOPROXY=github.com/klauspost/compress '
    $dockerfile += $build.Value.TrimEnd().Substring(4)
    $dockerfile += "`nFROM 3x-ui-local-dev`nCOPY --from=builder /app/build/x-ui /app/x-ui`nCOPY --from=builder /app/internal/web/translation /app/internal/web/translation`n"
    $dockerfile | docker build --progress plain -f - -t $image .
    if ($LASTEXITCODE -ne 0) { throw 'Build failed. The existing backend was not replaced.' }

    # The development container mounts src, but serves templates and public
    # assets from its own filesystem.
    foreach ($entry in @('index.html', 'login.html', 'subpage.html', 'public/.')) {
        $target = if ($entry -eq 'public/.') { 'public/' } else { $entry }
        Invoke-Docker @('cp', (Join-Path $project "frontend/$entry"), "${vite}:/src/frontend/$target")
    }

    Invoke-Docker @('stop', $backend)
    try {
        Invoke-Docker @('restart', $vite)
    } catch {
        Invoke-Docker @('start', $backend)
        throw
    }
    try {
        Invoke-Docker @('rename', $backend, $previous)
    } catch {
        Invoke-Docker @('start', $backend)
        throw
    }
    $created = $false
    try {
        Invoke-Docker @('create', '--name', $backend, '--network', "container:$vite", '--volumes-from', $previous,
            '-e', 'XUI_ENABLE_FAIL2BAN=false', '-e', 'XUI_DEBUG=true', '-e', 'XUI_INIT_WEB_BASE_PATH=/', $image)
        $created = $true
        Invoke-Docker @('start', $backend)
        $healthy = $false
        for ($attempt = 0; $attempt -lt 15; $attempt++) {
            try {
                $result = Invoke-RestMethod -Uri "$origin/csrf-token" -TimeoutSec 2
                if ($result.success -and $result.obj) {
                    $healthy = $true
                    break
                }
            } catch {
                Start-Sleep -Seconds 1
            }
        }
        if (!$healthy) { throw 'The new backend did not respond through Vite.' }
        Write-Host "Dev published at $origin/ (previous container: $previous)"
    } catch {
        $publishError = $_
        if ($created) {
            docker stop $backend | Out-Null
            Invoke-Docker @('rename', $backend, "$previous-failed")
        }
        Invoke-Docker @('rename', $previous, $backend)
        Invoke-Docker @('start', $backend)
        throw $publishError
    }
} finally {
    Pop-Location
}

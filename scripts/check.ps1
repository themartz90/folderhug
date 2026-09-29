$ErrorActionPreference = 'Stop'
Push-Location (Split-Path $PSScriptRoot -Parent)
try {
    Push-Location web
    try {
        npm ci
        if ($LASTEXITCODE -ne 0) { throw 'npm ci failed' }
        foreach ($step in @('check', 'lint', 'test:unit', 'build')) {
            npm run $step
            if ($LASTEXITCODE -ne 0) { throw "npm run $step failed" }
        }
    } finally { Pop-Location }
    go test ./...
    if ($LASTEXITCODE -ne 0) { throw 'Go tests failed' }
    go vet ./...
    if ($LASTEXITCODE -ne 0) { throw 'Go vet failed' }
    go build -o bin/http-wrapper.exe ./cmd/http-wrapper
    if ($LASTEXITCODE -ne 0) { throw 'Go build failed' }
} finally { Pop-Location }

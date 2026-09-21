# Run the Windows concierge regression without unrelated Unix-only test files.
# Invoke from the dearmachine Go module with the pinned native Go/CGO toolchain.
$ErrorActionPreference='Stop'
if ($env:OS -ne 'Windows_NT') { throw 'Native Windows required.' }
$package=(& go list -json ./cmd/dearmachine) | Out-String | ConvertFrom-Json
if ($LASTEXITCODE -ne 0) { throw 'Cannot list native concierge sources.' }
$files=@($package.GoFiles | ForEach-Object { Join-Path 'cmd\dearmachine' $_ })
$files+=Join-Path 'cmd\dearmachine' 'concierge_windows_test.go'
& go test -count=1 -v @files
if ($LASTEXITCODE -ne 0) { throw 'Native Windows concierge test failed.' }

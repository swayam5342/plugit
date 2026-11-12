# The README demo: add (picker, tool detail, response fields), lint, try,
# install. Record it with vhs (needs vhs, ttyd and ffmpeg), on Windows:
#
#   go build -o demo/plugit.exe ./cmd/plugit
#   cd demo && vhs demo.tape
#
# It runs offline: mock_api.py stands in for the Petstore API, the spec is
# the pinned copy in testdata/specs, and APPDATA/USERPROFILE point inside
# this folder, so no real MCP client config is touched (the tape stops if
# they don't). PowerShell, because its $env: settings reach plugit.exe.
Output demo.gif
Set Shell "pwsh"
Set FontSize 15
Set Width 1280
Set Height 760
Set Padding 18
Set TypingSpeed 35ms
Set Theme "Catppuccin Mocha"

Hide
Type `Remove-Item -Recurse -Force cfg, appdata, home -ErrorAction SilentlyContinue; $null = New-Item -ItemType Directory appdata/Claude, home; Set-Content appdata/Claude/claude_desktop_config.json '{"mcpServers": {}}'` Enter
Type "$env:APPDATA = Join-Path $PWD 'appdata'; $env:USERPROFILE = Join-Path $PWD 'home'; $env:HOME = $env:USERPROFILE" Enter
Type "if ($env:APPDATA -notlike '*\demo\appdata') { Write-Host 'UNSAFE-APPDATA'; exit 1 }" Enter
Type "$mock = Start-Process python -ArgumentList 'mock_api.py','47811' -NoNewWindow -PassThru" Enter
Sleep 1.5s
Type "try { $null = Invoke-RestMethod http://127.0.0.1:47811/api/v3/store/inventory } catch { Write-Host 'MOCK-NOT-RUNNING'; exit 1 }" Enter
Type "Copy-Item ../testdata/specs/petstore3.json petstore.json" Enter
Type "function plugit { & ./plugit.exe --config-dir ./cfg @args }" Enter
Type "function prompt { [char]27 + '[38;5;209m❯' + [char]27 + '[0m ' }" Enter
Sleep 1s
Type "Clear-Host" Enter
Sleep 500ms
Show

# 1. Add an API: pin the base URL, pick tools.
Type "plugit add pets petstore.json --base-url http://127.0.0.1:47811/api/v3 --allow-private" Sleep 400ms Enter
Sleep 2.5s
Screenshot shot-1-picker.png
Type "/" Sleep 300ms Type "pet" Sleep 300ms Enter
Sleep 800ms
Down Sleep 250ms Down Sleep 250ms Down
Sleep 800ms
# The tool detail: parameters, token cost, lint, input schema.
Type "i"
Sleep 3s
Screenshot shot-2-detail.png
Escape
Sleep 500ms
# Keep only a few response fields, to save tokens.
Type "f" Sleep 400ms Type "id, name, status" Sleep 300ms Enter
Sleep 1.2s
Screenshot shot-3-fields.png
Type "s"
Sleep 2s

# 2. Lint: quality and safety checks.
Type "plugit lint pets" Sleep 300ms Enter
Sleep 2s

# 3. One real call: the redacted request, and the filtered response.
Type "plugit try pets findPetsByStatus --arg status=available" Sleep 300ms Enter
Sleep 3.5s
Screenshot shot-4-try.png

# 4. Install into an MCP client: pick it, review the diff, confirm.
Type "Clear-Host" Enter
Type "plugit install pets" Sleep 300ms Enter
Sleep 2s
Screenshot shot-5-chooser.png
Enter
Sleep 1.5s
Type "y" Sleep 300ms Enter
Sleep 3s
Screenshot shot-6-install.png

Hide
Type "Stop-Process -Id $mock.Id; Remove-Item petstore.json" Enter

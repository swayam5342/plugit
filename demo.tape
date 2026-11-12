# Demo GIF for the README: `vhs demo.tape` (needs vhs, ttyd and ffmpeg).
# Records zero-config run, then add with the picker and install, against
# the public Petstore spec. Uses a throwaway config dir.
Output demo.gif
Set Shell "bash"
Set FontSize 16
Set Width 1200
Set Height 700
Set TypingSpeed 40ms

Hide
Type "export CFG=$(mktemp -d)" Enter
Show

Type "plugit --config-dir $CFG add pets https://petstore.swagger.io/v2/swagger.json" Sleep 500ms Enter
Sleep 3s
Type "y" Enter
Sleep 2s
Type "/" Sleep 300ms Type "pet" Enter Sleep 1s
Down Down Space Sleep 500ms
Type "r" Sleep 300ms Ctrl+U Type "find_pets" Enter Sleep 1s
Type "s"
Sleep 2s
Type "plugit --config-dir $CFG try pets findPetsByStatus --arg status=available | head -20" Enter
Sleep 4s
Type "plugit --config-dir $CFG install pets --client claude-desktop" Enter
Sleep 3s

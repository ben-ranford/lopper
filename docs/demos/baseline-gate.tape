Output docs/demos/assets/baseline-gate.gif
Set Shell "bash"
Set Width 1600
Set Height 980
Set FontSize 14
Set Padding 24
Set TypingSpeed 45ms
Set Framerate 30

Type "cd /vhs"
Enter
Sleep 300ms

Type "mkdir -p .artifacts/demos"
Enter
Sleep 300ms

Type "cp docs/demos/fixtures/baseline.json .artifacts/demos/baseline.json"
Enter
Sleep 1s

Type "function lopper() { cat docs/demos/fixtures/baseline-gate.table.txt; }"
Enter
Sleep 400ms

Type "lopper analyse --repo . --language go --cache=false --top 5 --format table --baseline docs/demos/fixtures/baseline.json --threshold-fail-on-increase 1"
Enter
Sleep 4s

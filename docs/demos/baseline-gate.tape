Output docs/demos/assets/baseline-gate.gif
Set Shell "bash"
Set Width 1200
Set Height 760
Set FontSize 16
Set Padding 24
Set TypingSpeed 45ms
Set Framerate 30

Type "cd /vhs"
Enter
Sleep 300ms

Type "mkdir -p .artifacts/demos"
Enter
Sleep 300ms

Type "go run ./cmd/lopper analyse --top 8 --repo . --language go --format json > .artifacts/demos/baseline.json"
Enter
Sleep 1s

Type "go run ./cmd/lopper analyse --top 8 --repo . --language go --format table --baseline .artifacts/demos/baseline.json --threshold-fail-on-increase 1"
Enter
Sleep 4s

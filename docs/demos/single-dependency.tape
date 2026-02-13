Output docs/demos/assets/single-dependency.gif
Set Shell "bash"
Set Width 1200
Set Height 760
Set FontSize 16
Set Padding 24
Set TypingSpeed 45ms
Set Framerate 30

Type "cd /vhs"
Enter
Sleep 400ms

Type "go run ./cmd/lopper analyse gopkg.in/yaml.v3 --repo . --language go --format table"
Enter
Sleep 4s

Output docs/demos/assets/single-dependency.gif
Set Shell "bash"
Set Width 1600
Set Height 980
Set FontSize 14
Set Padding 24
Set TypingSpeed 45ms
Set Framerate 30

Type "cd /vhs"
Enter
Sleep 400ms

Type "function lopper() { cat docs/demos/fixtures/single-dependency.table.txt; }"
Enter
Sleep 400ms

Type "lopper analyse gopkg.in/yaml.v3 --repo . --language go --cache=false --format table"
Enter
Sleep 4s

package cli

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"time"
)

var (
	handlers = map[string]func(p []string, sz int){}
)

func AddCallback(fun string, cb func(p []string, sz int)) {
	handlers[fun] = cb
}

func Line(line string) {
	fmt.Printf("\r\033[K")
	fmt.Println(line)
	fmt.Print("> ")
}

func Init() {
	reader := bufio.NewReader(os.Stdin)
	for {
		text, _ := reader.ReadString('\n')
		split := strings.Split(text[:len(text)-1], " ")
		sz := len(split)
		if sz > 0 {
			handler, ok := handlers[split[0]]
			if ok {
				handler(split, sz)
			} else {
				Line("Unknown command\nCommands:")
				for name := range handlers {
					Line(name)
				}
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
}

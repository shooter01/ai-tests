package main

import (
	"fmt"
	"os"
	ticket2 "test-claude/pkg/ticket2"
)

func main() {
	ticket := ParseTicket(55555552423)
	fmt.Println("Ticket:", ticket)

	data, _ := os.ReadFile("test.txt")

	lines := ReadLines(string(data))
	fmt.Println("Line from file:", lines[1])

	t := ticket2.Ticket{
		ID:       123,
		Priority: "HIGH1",
		Assignee: "shooter01",
	}
	result := ticket2.Process(t)
	fmt.Println(result)
}

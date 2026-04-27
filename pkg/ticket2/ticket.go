package ticket2

import "fmt"

type Ticket struct {
	ID       string
	Priority int
	Assignee string
}

func Process(t Ticket) string {
	return fmt.Sprintf("[%s] assigned to %s (priority: %d)", t.ID, t.Assignee, t.Priority)
}

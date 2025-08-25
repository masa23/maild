package imapsession

import (
	"log"

	"github.com/k0kubun/pp/v3"

	"github.com/emersion/go-imap/v2/imapserver"
)

// Login implements the IMAP LOGIN command
func (s *Session) Login(username, password string) error {
	log.Println(pp.Sprintf("Login called with username: %s", username))
	// For now, we'll accept any username/password combination
	// In a real implementation, this would validate against a user database
	s.username = username
	return nil
}

// Logout implements the IMAP LOGOUT command
func (s *Session) Logout() error {
	log.Println(pp.Sprintf("Logout called"))
	// Reset session state
	s.username = ""
	s.mailbox = ""
	s.mailboxID = 0
	return nil
}

// Close implements the IMAP CLOSE command
func (s *Session) Close() error {
	log.Println(pp.Sprintf("Close called"))
	return nil
}

// Unselect implements the IMAP UNSELECT command
func (s *Session) Unselect() error {
	log.Println(pp.Sprintf("Unselect called"))
	return nil
}

// Idle implements the IMAP IDLE command
func (s *Session) Idle(w *imapserver.UpdateWriter, stop <-chan struct{}) error {
	log.Println(pp.Sprintf("Idle called"))
	// Simple implementation that just waits for stop signal
	<-stop
	return nil
}

// Poll implements the IMAP POLL command
func (s *Session) Poll(w *imapserver.UpdateWriter, allowExpunge bool) error {
	log.Println(pp.Sprintf("Poll called with allowExpunge: %v", allowExpunge))
	return nil
}

package imapsession

import (
	"errors"
	"log"

	"github.com/k0kubun/pp/v3"

	"github.com/emersion/go-imap/v2/imapserver"
)

// Login implements the IMAP LOGIN command
func (s *Session) Login(username, password string) error {
	log.Println(pp.Sprintf("Login called with username: %s", username))
	// ToDo: authentication process
	s.username = "test@mail.masa23.jp"
	return nil
}

// Logout implements the IMAP LOGOUT command
func (s *Session) Logout() error {
	log.Println(pp.Sprintf("Logout called"))
	return errors.New("Logout is not implemented")
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
	return errors.New("Idle is not implemented")
}

// Poll implements the IMAP POLL command
func (s *Session) Poll(w *imapserver.UpdateWriter, allowExpunge bool) error {
	log.Println(pp.Sprintf("Poll called with allowExpunge: %v", allowExpunge))
	return nil
}

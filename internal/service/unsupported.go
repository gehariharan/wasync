package service

type unsupported struct{}

func (unsupported) Where() string            { return "" }
func (unsupported) Install(Spec) error       { return ErrUnsupported }
func (unsupported) Uninstall() error         { return ErrUnsupported }
func (unsupported) Installed() (bool, error) { return false, nil }

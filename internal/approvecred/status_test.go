package approvecred

import (
	"errors"
	"strings"
	"testing"
)

// script answers each Keychain call with the next result for it and counts calls.
type script struct {
	adds, updates, removes []int
	calls                  []string
}

func next(list *[]int) int {
	if len(*list) == 0 {
		return statusOK
	}
	v := (*list)[0]
	*list = (*list)[1:]
	return v
}

func (s *script) add(string, string, string) int {
	s.calls = append(s.calls, "add")
	return next(&s.adds)
}
func (s *script) update(string, string, string) int {
	s.calls = append(s.calls, "update")
	return next(&s.updates)
}
func (s *script) remove(string) int { s.calls = append(s.calls, "remove"); return next(&s.removes) }

func calls(s *script) string { return strings.Join(s.calls, ",") }

func TestANewItemIsAddedOnce(t *testing.T) {
	s := &script{}
	if err := setItem(s, "approve:a", legacy, "/d"); err != nil || calls(s) != "add" {
		t.Fatalf("%v %s", err, calls(s))
	}
}

func TestAnExistingItemIsUpdatedInPlace(t *testing.T) {
	s := &script{adds: []int{statusDuplicate}}
	if err := setItem(s, "approve:a", legacy, "/d"); err != nil || calls(s) != "add,update" {
		t.Fatalf("%v %s", err, calls(s))
	}
}

// -25244: macOS will not let this build edit an item another build made.
func TestAnItemAnotherBuildMadeIsReplacedOnce(t *testing.T) {
	s := &script{adds: []int{statusDuplicate, statusOK}, updates: []int{statusInvalidOwnerEdit}}
	if err := setItem(s, "approve:a", legacy, "/d"); err != nil || calls(s) != "add,update,remove,add" {
		t.Fatalf("%v %s", err, calls(s))
	}
}

func TestAnItemThatCannotBeReplacedSaysHowToFixIt(t *testing.T) {
	s := &script{adds: []int{statusDuplicate}, updates: []int{statusInvalidOwnerEdit}, removes: []int{statusInvalidOwnerEdit}}
	err := setItem(s, "approve:a", legacy, "/d")
	if !errors.Is(err, ErrForeignItem) || !strings.Contains(err.Error(), "approve:a") || !strings.Contains(err.Error(), "Keychain Access") {
		t.Fatalf("%v", err)
	}
	if calls(s) != "add,update,remove" {
		t.Fatalf("kept going after a failed replace: %s", calls(s))
	}
}

func TestAReplacementThatIsRefusedAgainStopsThere(t *testing.T) {
	s := &script{adds: []int{statusDuplicate, statusInvalidOwnerEdit}, updates: []int{statusInvalidOwnerEdit}}
	if err := setItem(s, "approve:a", legacy, "/d"); !errors.Is(err, ErrForeignItem) || calls(s) != "add,update,remove,add" {
		t.Fatalf("%v %s", err, calls(s))
	}
}

// A "no" from the person is never asked again.
func TestADeniedWriteIsNeverRetried(t *testing.T) {
	for _, status := range []int{statusUserCanceled, statusAuthFailed, statusInteractionNotAllowed} {
		s := &script{adds: []int{statusDuplicate}, updates: []int{status}}
		if err := setItem(s, "approve:a", legacy, "/d"); !errors.Is(err, ErrAccessDenied) || calls(s) != "add,update" {
			t.Fatalf("status %d: %v %s", status, err, calls(s))
		}
	}
	s := &script{adds: []int{statusDuplicate}, updates: []int{statusInvalidOwnerEdit}, removes: []int{statusUserCanceled}}
	if err := setItem(s, "approve:a", legacy, "/d"); !errors.Is(err, ErrAccessDenied) || calls(s) != "add,update,remove" {
		t.Fatalf("%v %s", err, calls(s))
	}
}

func TestAnotherWriteFailureNamesTheSystemCode(t *testing.T) {
	s := &script{adds: []int{-67671}}
	err := setItem(s, "approve:a", legacy, "/d")
	if err == nil || !strings.Contains(err.Error(), "-67671") || calls(s) != "add" {
		t.Fatalf("%v %s", err, calls(s))
	}
}

func TestReadAndDeleteResultsMapToTheErrorsWasyncKnows(t *testing.T) {
	if !errors.Is(readError(statusNotFound), ErrNotFound) {
		t.Fatal("not found")
	}
	for _, status := range []int{statusUserCanceled, statusAuthFailed, statusInteractionNotAllowed} {
		if !errors.Is(readError(status), ErrAccessDenied) || !errors.Is(deleteError(status), ErrAccessDenied) {
			t.Fatalf("status %d is not a denial", status)
		}
	}
	if err := readError(-25244); err == nil || errors.Is(err, ErrAccessDenied) || !strings.Contains(err.Error(), "-25244") {
		t.Fatalf("%v", err)
	}
	if readError(statusTooLong) == nil {
		t.Fatal("a value that does not fit must be an error")
	}
	if deleteError(statusOK) != nil || deleteError(statusNotFound) != nil {
		t.Fatal("removing what is not there is not an error")
	}
	if deleteError(-1) == nil {
		t.Fatal("a failed delete must be reported")
	}
}

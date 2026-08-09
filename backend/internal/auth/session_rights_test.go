package auth

import "testing"

// Watching a run think and steering it are TWO rights, and holding the first must never confer the
// second. That is the whole point of the split: someone may be given a window into a run without
// being given a hand in it.
func TestWatchingASessionIsNotWritingIntoIt(t *testing.T) {
	watcher := &User{Username: "ada", Groups: []string{devlabGroup, sessionWatchGroup}}
	if !watcher.CanWatchSession() {
		t.Error("a watcher cannot watch")
	}
	if watcher.CanSpeakInSession() {
		t.Fatal("the watch right also allows writing — the two rights are one, and the split is a fiction")
	}

	// The reverse DOES hold: steering something one cannot see is not a right anybody could use.
	speaker := &User{Username: "bo", Groups: []string{devlabGroup, sessionSpeakGroup}}
	if !speaker.CanSpeakInSession() || !speaker.CanWatchSession() {
		t.Errorf("a speaker must also be able to read: watch=%v speak=%v", speaker.CanWatchSession(), speaker.CanSpeakInSession())
	}

	// Neither right comes with the mere entry right.
	plain := &User{Username: "cy", Groups: []string{devlabGroup}}
	if plain.CanWatchSession() || plain.CanSpeakInSession() {
		t.Error("the DevLab entry right hands out the session rights on its own")
	}

	// An admin holds every right, as everywhere in the landscape.
	admin := &User{Username: "root", IsAdmin: true}
	if !admin.CanWatchSession() || !admin.CanSpeakInSession() {
		t.Error("an admin is refused a right")
	}
}

// Each right is backed one-to-one by its own Linux group, under the shared naming.
func TestEachSessionRightHasItsOwnBackingGroup(t *testing.T) {
	if sessionWatchGroup == sessionSpeakGroup {
		t.Fatal("both rights are backed by ONE group — granting either grants both")
	}
	for _, g := range []string{sessionWatchGroup, sessionSpeakGroup} {
		if len(g) < len("hp_devlab_") || g[:len("hp_devlab_")] != "hp_devlab_" {
			t.Errorf("%q does not follow the service's group naming", g)
		}
	}
}

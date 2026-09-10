package api

import (
	"crypto/rand"
	"fmt"
	"strings"
	"time"
)

// alphabet is lowercase letters and digits only.
//
// A run's id becomes the id of an autoscaler target, which in turn becomes
// part of Deployment, container and Secret names. Anything outside this set is
// illegal somewhere down there, so it is excluded here rather than discovered
// at the moment a run tries to provision.
const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"

// newID makes a time-ordered, unguessable identifier.
//
// Time-ordered so that ids sort the way runs happened, which makes a database
// listing and a directory of exports both read sensibly. Random-suffixed so
// two runs started in the same millisecond cannot collide.
func newID(prefix string) (string, error) {
	suffix, err := randomString(8)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s-%s-%s", prefix, base36(time.Now().UTC().UnixMilli()), suffix), nil
}

func base36(n int64) string {
	if n <= 0 {
		return "0"
	}
	var out []byte
	for n > 0 {
		out = append([]byte{alphabet[n%36]}, out...)
		n /= 36
	}
	return string(out)
}

func randomString(length int) (string, error) {
	bytes := make([]byte, length)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("generating an identifier: %w", err)
	}

	var out strings.Builder
	for _, b := range bytes {
		out.WriteByte(alphabet[int(b)%len(alphabet)])
	}
	return out.String(), nil
}

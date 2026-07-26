package axiomrepo

import "encoding/base64"

// base64Std is the encoding git's Basic authorization header expects.
func base64Std(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

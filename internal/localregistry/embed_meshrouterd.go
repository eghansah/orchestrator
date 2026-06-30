//go:build with_meshrouterd_image

package localregistry

import _ "embed"

//go:embed images/meshrouterd.tar
var MeshrouterdTar []byte

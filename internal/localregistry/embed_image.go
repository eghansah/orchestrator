//go:build with_ingressd_image

package localregistry

import _ "embed"

//go:embed images/ingressd.tar
var IngressdTar []byte

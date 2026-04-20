package contracts

import "fmt"

// Tagged unions in this package use value-receiver marker methods, so both T
// and *T satisfy the marker interface. The variant metadata helpers therefore
// normalize both representations instead of rejecting pointer-backed variants
// before discriminator symmetry checks run.
func validateTaggedUnionDiscriminator[T ~string](outer, variant, inner T, variantTypeErr, innerFieldErr error) error {
	if outer != variant {
		return fmt.Errorf("%w: outer=%s variant=%s", variantTypeErr, outer, variant)
	}
	if outer != inner {
		return fmt.Errorf("%w: outer=%s inner=%s", innerFieldErr, outer, inner)
	}
	return nil
}

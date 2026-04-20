package contracts

import "fmt"

func validateTaggedUnionDiscriminator[T ~string](outer, variant, inner T, variantTypeErr, innerFieldErr error) error {
	if outer != variant {
		return fmt.Errorf("%w: outer=%s variant=%s", variantTypeErr, outer, variant)
	}
	if outer != inner {
		return fmt.Errorf("%w: outer=%s inner=%s", innerFieldErr, outer, inner)
	}
	return nil
}

package jsoncontract

type Internal struct {
	prepared
}

type Function struct {
	prepared
}

type Structured struct {
	prepared
}

func (p Preparer) Internal(owner string, source any, customizers ...Customize) (Internal, error) {
	value, err := p.prepare(owner, source, profileInternal, customizers)
	return Internal{prepared: value}, err
}

func (p Preparer) Function(owner string, source any, customizers ...Customize) (Function, error) {
	value, err := p.prepare(owner, source, profileFunction, customizers)
	return Function{prepared: value}, err
}

func (p Preparer) Structured(owner string, source any, customizers ...Customize) (Structured, error) {
	value, err := p.prepare(owner, source, profileStructured, customizers)
	return Structured{prepared: value}, err
}

type profile uint8

const (
	profileInternal profile = iota
	profileFunction
	profileStructured
)

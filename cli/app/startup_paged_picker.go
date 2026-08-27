package app

const startupPickerResidentPageLimit = 2

type startupPickerPageDirection uint8

const (
	startupPickerPageInitial startupPickerPageDirection = iota + 1
	startupPickerPageNext
	startupPickerPagePrevious
)

type startupPickerPageRequest struct {
	generation      uint64
	offset          int
	direction       startupPickerPageDirection
	crossing        bool
	pageMove        bool
	visibleDistance int
	move            int
}

type startupPickerEdgeState uint8

const (
	startupPickerEdgeUnknown startupPickerEdgeState = iota + 1
	startupPickerEdgeLoading
	startupPickerEdgeExhausted
	startupPickerEdgeFailed
)

type startupPickerPageEdge struct {
	state           startupPickerEdgeState
	requestedOffset int
	generation      uint64
	diagnostic      error
	request         *startupPickerPageRequest
	failedRequest   *startupPickerPageRequest
}

type startupPickerPageWindow[P any] struct {
	segments     []P
	request      *startupPickerPageRequest
	generation   uint64
	nextEdge     startupPickerPageEdge
	previousEdge startupPickerPageEdge
}

func appendBoundedPickerPage[T any](pages []T, page T) []T {
	pages = append(pages, page)
	if len(pages) > startupPickerResidentPageLimit {
		pages = pages[len(pages)-startupPickerResidentPageLimit:]
	}
	return pages
}

func prependBoundedPickerPage[T any](pages []T, page T) []T {
	pages = append([]T{page}, pages...)
	if len(pages) > startupPickerResidentPageLimit {
		pages = pages[:startupPickerResidentPageLimit]
	}
	return pages
}

func flattenBoundedPickerPages[P, T any](pages []P, items func(P) []T) []T {
	count := 0
	for _, page := range pages {
		count += len(items(page))
	}
	result := make([]T, 0, count)
	for _, page := range pages {
		result = append(result, items(page)...)
	}
	return result
}

func (w *startupPickerPageWindow[P]) reset() {
	w.segments = nil
	w.request = nil
	w.nextEdge = startupPickerPageEdge{}
	w.previousEdge = startupPickerPageEdge{}
}

func (w *startupPickerPageWindow[P]) begin(
	direction startupPickerPageDirection,
	offset int,
	crossing bool,
	pageMove bool,
	visibleDistance int,
	move int,
) (startupPickerPageRequest, bool) {
	if w.requestFor(direction) != nil {
		return startupPickerPageRequest{}, false
	}
	w.generation++
	request := startupPickerPageRequest{
		generation:      w.generation,
		offset:          offset,
		direction:       direction,
		crossing:        crossing,
		pageMove:        pageMove,
		visibleDistance: visibleDistance,
		move:            move,
	}
	if direction == startupPickerPageInitial {
		w.request = &request
	} else {
		edge := w.edge(direction)
		edge.request = &request
		edge.state = startupPickerEdgeLoading
		edge.requestedOffset = offset
		edge.generation = request.generation
	}
	return request, true
}

func (w *startupPickerPageWindow[P]) retry(
	direction startupPickerPageDirection,
	crossing bool,
	pageMove bool,
	visibleDistance int,
) (startupPickerPageRequest, bool) {
	edge := w.edge(direction)
	if edge.failedRequest == nil {
		return startupPickerPageRequest{}, false
	}
	if visibleDistance < 1 {
		visibleDistance = edge.failedRequest.visibleDistance
	}
	return w.begin(direction, edge.failedRequest.offset, crossing, pageMove, visibleDistance, edge.failedRequest.move)
}

func (w *startupPickerPageWindow[P]) requestFor(direction startupPickerPageDirection) *startupPickerPageRequest {
	if direction == startupPickerPageInitial {
		return w.request
	}
	return w.edge(direction).request
}

func (w *startupPickerPageWindow[P]) hasPendingRequest() bool {
	return w.request != nil ||
		w.previousEdge.request != nil ||
		w.nextEdge.request != nil
}

func (w *startupPickerPageWindow[P]) complete(request startupPickerPageRequest) bool {
	active := w.requestFor(request.direction)
	if active == nil || *active != request {
		return false
	}
	if request.direction == startupPickerPageInitial {
		w.request = nil
	} else {
		edge := w.edge(request.direction)
		edge.request = nil
		edge.failedRequest = nil
		edge.diagnostic = nil
	}
	return true
}

func (w *startupPickerPageWindow[P]) fail(request startupPickerPageRequest, diagnostic error) bool {
	active := w.requestFor(request.direction)
	if active == nil || *active != request {
		return false
	}
	if request.direction == startupPickerPageInitial {
		w.request = nil
	} else {
		edge := w.edge(request.direction)
		edge.request = nil
		edge.state = startupPickerEdgeFailed
		edge.requestedOffset = request.offset
		edge.generation = request.generation
		edge.diagnostic = diagnostic
		failed := request
		edge.failedRequest = &failed
	}
	return true
}

func (w *startupPickerPageWindow[P]) edge(direction startupPickerPageDirection) *startupPickerPageEdge {
	switch direction {
	case startupPickerPagePrevious:
		return &w.previousEdge
	case startupPickerPageNext:
		return &w.nextEdge
	default:
		panic("startup picker initial page has no directional edge")
	}
}

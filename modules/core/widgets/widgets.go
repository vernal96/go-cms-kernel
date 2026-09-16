package widgets

import "github.com/vernal96/go-cms-kernel/modules/core/widget"

// All returns core widgets that do not require runtime services. Runtime
// assembly appends service-backed widgets so they remain site-scoped.
func All() []widget.Widget {
	return []widget.Widget{
		contentWidget(),
		htmlWidget(),
	}
}

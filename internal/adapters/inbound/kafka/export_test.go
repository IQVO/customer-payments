package kafka

// The dispatch tables and the poison marker, for the external tests.
var (
	CheckoutHandlers      = checkoutHandlers
	OrderSourcingHandlers = orderSourcingHandlers
	ShippingHandlers      = shippingHandlers
	ErrPoison             = errPoison
)

Bug report from support: when a coupon is worth more than the order, `ApplyDiscount`
returns a negative total — for example a 500-cent order with an 800-cent coupon comes out
at -300, and the customer is shown a refund. Please fix `ApplyDiscount` so an order total
can never go below zero.

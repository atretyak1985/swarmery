Add a `LineItem` type and a `ParseLineItem` function to the orders package, in a new file
`lineitem.go`:

```go
type LineItem struct {
	SKU string
	Qty int
}

func ParseLineItem(s string) (LineItem, error)
```

`ParseLineItem` parses one line item written as `<sku>:<qty>`, for example `A-100:3`.
Surrounding whitespace is ignored. It returns an error when there is no `:`, when the SKU is
empty, or when the quantity is not a positive whole number. Add tests for it.

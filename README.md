mapdecode
=========

mapdecode implements a generic `interface{}` decoder. It allows implementing
custom YAML/JSON decoding logic only once. Instead of implementing the same
`UnmarshalYAML` and `UnmarshalJSON` twice, you can implement `Decode` once,
parse the YAML/JSON input into a `map[string]interface{}` and decode it using
this package.

```go
var data map[string]interface{}
if err := json.Decode(&data, input); err != nil {
    log.Fatal(err)
}

var result MyStruct
if err := mapdecode.Decode(&result, data); err != nil {
    log.Fatal(err)
}
```

This package relies heavily on [mapstructure] for much of its functionality.

  [mapstructure]: https://github.com/mitchellh/mapstructure

Status
------

Beta

-------------------------------------------------------------------------------

Released under the [MIT License].

  [MIT License]: LICENSE.txt

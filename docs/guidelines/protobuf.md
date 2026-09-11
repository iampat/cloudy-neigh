# Protobuf guidelines

These rules bind all Protocol Buffer schemas in this repository.

## Message design

- Do not use bare primitive types for repeated fields or map values unless the type will never change.
- Wrap values in a message. Wrapping preserves backward compatibility and allows adding fields later without breaking wire compatibility.
- Map keys must remain primitive types per protobuf specification.
- Field tag numbers are permanent once published. Never reuse a tag number.
- Use `reserved` for deleted tag numbers and field names.
- Do not add speculative fields. Comment out unused variants until needed.

package i18n

import "errors"

// Keyed is implemented by structured errors that carry a translation key and public args.
type Keyed interface {
	I18nKey() (string, map[string]string)
}

// KeyedError represents a localizable error resolved at presentation boundaries.
type KeyedError struct {
	Key      string
	Args     map[string]string
	Fallback string
	Cause    error
}

func (e *KeyedError) Error() string {
	if e == nil {
		return ""
	}
	if e.Key != "" && HasKey(e.Key) {
		return T(Effective(), e.Key, e.Args)
	}
	if e.Fallback != "" {
		return e.Fallback
	}
	if e.Cause != nil {
		return e.Cause.Error()
	}
	return e.Key
}

func (e *KeyedError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func (e *KeyedError) I18nKey() (string, map[string]string) {
	if e == nil {
		return "", nil
	}
	return e.Key, CloneArgs(e.Args)
}

// CloneArgs returns a shallow copy of args, or nil when empty.
func CloneArgs(args map[string]string) map[string]string {
	if len(args) == 0 {
		return nil
	}
	cp := make(map[string]string, len(args))
	for k, v := range args {
		cp[k] = v
	}
	return cp
}

// Err constructs a KeyedError without arguments.
func Err(key string) *KeyedError {
	return &KeyedError{Key: key}
}

// ErrArgs constructs a KeyedError with public display arguments.
func ErrArgs(key string, args map[string]string) *KeyedError {
	return &KeyedError{Key: key, Args: CloneArgs(args)}
}

// Wrap constructs a KeyedError that wraps cause for errors.Is / errors.As while localizing display text.
func Wrap(key string, args map[string]string, cause error) *KeyedError {
	cloned := CloneArgs(args)
	if cause != nil {
		if cloned == nil {
			cloned = map[string]string{}
		}
		if _, exists := cloned["err"]; !exists {
			cloned["err"] = cause.Error()
		}
	}
	return &KeyedError{Key: key, Args: cloned, Cause: cause}
}

// FromKey constructs an error from wire/persisted key, args, and fallback message.
func FromKey(key string, args map[string]string, fallback string) error {
	if key == "" {
		return errors.New(fallback)
	}
	return &KeyedError{Key: key, Args: CloneArgs(args), Fallback: fallback}
}

// ErrorDetails extracts the outermost translation key and args from err if available.
func ErrorDetails(err error) (string, map[string]string, bool) {
	if err == nil {
		return "", nil, false
	}
	var k Keyed
	if errors.As(err, &k) {
		key, args := k.I18nKey()
		if key != "" {
			return key, CloneArgs(args), true
		}
	}
	return "", nil, false
}

// LocalizeError resolves err into localized text for locale, plus optional key and args.
func LocalizeError(locale string, err error) (message string, key string, args map[string]string) {
	if err == nil {
		return "", "", nil
	}
	key, args, _ = ErrorDetails(err)
	if key != "" && HasKey(key) {
		return T(locale, key, args), key, args
	}
	return err.Error(), key, args
}

// LocalizeMessage resolves an optional key+args pair with fallback text for locale.
func LocalizeMessage(locale, key string, args map[string]string, fallback string) string {
	if key != "" && HasKey(key) {
		return T(locale, key, args)
	}
	return fallback
}

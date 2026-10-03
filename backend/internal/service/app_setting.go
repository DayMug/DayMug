package service

import "context"

// AppSettingStore is the narrowed slice of store.Store that the app_settings
// use cases below need. Declared as a local interface (rather than taking
// store.Store) for the same reason pricing.AppSettingStore is: these are two
// key/value calls, so depending on the full store interface would buy nothing
// and would force every future stub to grow with it.
type AppSettingStore interface {
	GetAppSetting(ctx context.Context, key string) (string, error)
	SetAppSetting(ctx context.Context, key, value string) error
}

// HelpDocKey backs the admin-configurable help popup.
const HelpDocKey = "help_doc_markdown"

// HelpDoc returns the configured help markdown. An empty string is a normal
// state, not an error: it means no doc has been set, and the frontend uses it
// as the signal to hide the sidebar help button.
//
// A nil store degrades to "no doc configured" rather than panicking, matching
// pricing.Load's tolerance for unwired test/bootstrap paths.
func HelpDoc(ctx context.Context, s AppSettingStore) (string, error) {
	if s == nil {
		return "", nil
	}
	v, err := s.GetAppSetting(ctx, HelpDocKey)
	if err != nil {
		// Msg is the bare store message because that is exactly what this
		// endpoint has always put in the body; do not dress it up here.
		return "", Internal(err.Error(), err)
	}
	return v, nil
}

// SaveHelpDoc replaces the stored markdown. Storing an empty string is
// allowed and is how an admin "deletes" the help doc — the sidebar button
// hides itself when the value is empty, so there is no separate delete verb.
func SaveHelpDoc(ctx context.Context, s AppSettingStore, markdown string) error {
	if s == nil {
		return Internal("app setting store unavailable", nil)
	}
	if err := s.SetAppSetting(ctx, HelpDocKey, markdown); err != nil {
		return Internal(err.Error(), err)
	}
	return nil
}

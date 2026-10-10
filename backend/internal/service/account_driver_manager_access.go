package service

// AccountDriverRuntime exposes the same supervised runtime used by inference.
// Administrative operations never create a second driver manager.
func (m *PluginManager) AccountDriverRuntime() *AccountDriverManager {
	if m == nil {
		return nil
	}
	return m.accountDriverManager
}

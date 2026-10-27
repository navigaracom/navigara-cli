package main

import (
	"errors"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

var errConnRefused error = windows.WSAECONNREFUSED

// userEnvKey is the registry key of the user environment. Tests point it at a scratch key.
var userEnvKey = "Environment"

var sendMessageTimeout = windows.NewLazySystemDLL("user32.dll").NewProc("SendMessageTimeoutW")

// getUserEnv reads a saved user environment variable, not the one of this process.
func getUserEnv(name string) (string, bool, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, userEnvKey, registry.QUERY_VALUE)
	if err != nil {
		return "", false, err
	}
	defer k.Close()
	value, _, err := k.GetStringValue(name)
	if errors.Is(err, registry.ErrNotExist) {
		return "", false, nil
	}
	return value, err == nil, err
}

// getMachineEnv reads a machine environment variable. A user variable with the same name hides it.
func getMachineEnv(name string) string {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SYSTEM\CurrentControlSet\Control\Session Manager\Environment`, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer k.Close()
	value, _, _ := k.GetStringValue(name)
	return value
}

// setUserEnv saves a user environment variable, or deletes it when value is empty.
// Like setx, it tells running programs, so that new terminals get the value.
func setUserEnv(name, value string) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, userEnvKey, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	switch _, kind, _ := k.GetValue(name, nil); {
	case value == "":
		if err = k.DeleteValue(name); errors.Is(err, registry.ErrNotExist) {
			err = nil
		}
	case kind == registry.EXPAND_SZ:
		// Keep %VARIABLES% in the value expandable.
		err = k.SetExpandStringValue(name, value)
	default:
		err = k.SetStringValue(name, value)
	}
	if err != nil {
		return err
	}
	const hwndBroadcast, wmSettingChange, smtoAbortIfHung = 0xffff, 0x001a, 0x0002
	env, _ := windows.UTF16PtrFromString("Environment")
	_, _, _ = sendMessageTimeout.Call(hwndBroadcast, wmSettingChange, 0, uintptr(unsafe.Pointer(env)), smtoAbortIfHung, 5000, 0)
	return nil
}

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

// envOwnerKey records, per user variable, the enduser.id that setup wrote (value name) and the one it
// replaced (name + previousSuffix). Tests point it at a scratch key.
var envOwnerKey = `Software\Navigara`

// getEnvOwner returns a value of the record of the user variable name.
func getEnvOwner(name string) string {
	k, err := registry.OpenKey(registry.CURRENT_USER, envOwnerKey, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer k.Close()
	value, _, _ := k.GetStringValue(name)
	return value
}

// setEnvOwner sets a value of the record of the user variable name, or deletes it when id is empty.
func setEnvOwner(name, id string) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, envOwnerKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if id == "" {
		if err = k.DeleteValue(name); errors.Is(err, registry.ErrNotExist) {
			err = nil
		}
		return err
	}
	return k.SetStringValue(name, id)
}

// claudePolicyKeys are the registry policies of Claude Code. Tests point them at scratch keys.
var claudePolicyKeys = []struct {
	root       registry.Key
	name, path string
}{
	{registry.LOCAL_MACHINE, "HKLM", `SOFTWARE\Policies\ClaudeCode`},
	{registry.CURRENT_USER, "HKCU", `SOFTWARE\Policies\ClaudeCode`},
}

// claudePolicy names the registry policy that manages Claude Code, or is "" when there is none.
func claudePolicy() string {
	for _, p := range claudePolicyKeys {
		k, err := registry.OpenKey(p.root, p.path, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		_, _, err = k.GetValue("Settings", nil)
		k.Close()
		if err == nil {
			return p.name + `\` + p.path + `\Settings`
		}
	}
	return ""
}

// machineEnvKey is the registry key of the machine environment. Tests point it at a scratch key.
var machineEnvKey = struct {
	root registry.Key
	path string
}{registry.LOCAL_MACHINE, `SYSTEM\CurrentControlSet\Control\Session Manager\Environment`}

// getMachineEnv reads a machine environment variable. A user variable with the same name hides it.
func getMachineEnv(name string) string {
	k, err := registry.OpenKey(machineEnvKey.root, machineEnvKey.path, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer k.Close()
	value, _, _ := k.GetStringValue(name)
	return value
}

// machineEnvKind is the registry type of a machine environment variable.
func machineEnvKind(name string) uint32 {
	k, err := registry.OpenKey(machineEnvKey.root, machineEnvKey.path, registry.QUERY_VALUE)
	if err != nil {
		return registry.SZ
	}
	defer k.Close()
	_, kind, _ := k.GetValue(name, nil)
	return kind
}

// setUserEnv saves a user environment variable, or deletes it when value is empty.
// Like setx, it tells running programs, so that new terminals get the value.
func setUserEnv(name, value string) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, userEnvKey, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	_, kind, err := k.GetValue(name, nil)
	if errors.Is(err, registry.ErrNotExist) {
		kind = machineEnvKind(name)
	}
	switch {
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
	_, _, _ = sendMessageTimeout.Call(hwndBroadcast, wmSettingChange, 0, uintptr(unsafe.Pointer(env)), smtoAbortIfHung, 1000, 0)
	return nil
}

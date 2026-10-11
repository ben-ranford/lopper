//go:build windows

package main

import (
	"context"
	"debug/pe"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

var moduleFilename = windows.NewLazySystemDLL("kernel32.dll").NewProc("K32GetModuleFileNameExW")

type heldProcess struct {
	handle  windows.Handle
	created windows.Filetime
}

func requireAMD64Process(handle windows.Handle) error {
	var machine, native uint16
	if err := windows.IsWow64Process2(handle, &machine, &native); err != nil {
		return err
	}
	if machine != 0 || native != pe.IMAGE_FILE_MACHINE_AMD64 {
		return errors.New("provider requires native AMD64 process")
	}
	return nil
}

func observeProcess(process *os.Process, executable string) (heldProcess, error) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_INFORMATION|windows.PROCESS_VM_READ|windows.SYNCHRONIZE, false, uint32(process.Pid))
	if err != nil {
		return heldProcess{}, err
	}
	owned := heldProcess{handle: h}
	if err := owned.bind(executable); err != nil {
		return heldProcess{}, errors.Join(err, windows.CloseHandle(h))
	}
	return owned, nil
}

func (p *heldProcess) bind(executable string) error {
	if err := requireAMD64Process(p.handle); err != nil {
		return err
	}
	var exit, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(p.handle, &p.created, &exit, &kernel, &user); err != nil {
		return err
	}
	name, err := modulePath(p.handle, 0)
	if err != nil {
		return err
	}
	if !strings.EqualFold(name, executable) {
		return errors.New("owned probe image mismatch")
	}
	return p.alive()
}

func (p *heldProcess) alive() error {
	status, err := windows.WaitForSingleObject(p.handle, 0)
	if err != nil {
		return err
	}
	if status != uint32(windows.WAIT_TIMEOUT) {
		return errors.New("owned probe exited before release")
	}
	var created, exit, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(p.handle, &created, &exit, &kernel, &user); err != nil {
		return err
	}
	if created != p.created {
		return errors.New("owned probe creation identity changed")
	}
	return nil
}

func modulePath(process, module windows.Handle) (string, error) {
	var buffer [maxPathUnits + 1]uint16
	n, _, err := moduleFilename.Call(uintptr(process), uintptr(module), uintptr(unsafe.Pointer(&buffer[0])), uintptr(len(buffer)))
	if sizeErr := modulePathLength(n); sizeErr != nil {
		return "", errors.Join(sizeErr, err)
	}
	return windows.UTF16ToString(buffer[:n]), nil
}

func moduleHandles(process windows.Handle) ([]windows.Handle, error) {
	var handles [maxModules]windows.Handle
	var needed uint32
	width := uint32(unsafe.Sizeof(handles[0]))
	capacity := uint32(len(handles)) * width
	if err := windows.EnumProcessModulesEx(process, &handles[0], capacity, &needed, windows.LIST_MODULES_ALL); err != nil {
		return nil, err
	}
	count, err := moduleCount(needed, width)
	if err != nil {
		return nil, err
	}
	return handles[:count], nil
}

func moduleRecord(ctx context.Context, path string) (row record, returnErr error) {
	if err := canonicalRegular(path); err != nil {
		return record{}, err
	}
	file, err := os.Open(path)
	if err != nil {
		return record{}, err
	}
	defer func() { returnErr = errors.Join(returnErr, file.Close()) }()
	final, err := openedPath(file)
	if err != nil {
		return record{}, err
	}
	before, err := file.Stat()
	if err != nil {
		return record{}, err
	}
	hash, err := hashRegular(ctx, final, before.Size())
	if err != nil {
		return record{}, err
	}
	current, err := os.Lstat(final)
	if err != nil {
		return record{}, err
	}
	if !os.SameFile(before, current) {
		return record{}, errors.New("module identity changed during hash")
	}
	identity, err := fileIdentity(file)
	if err != nil {
		return record{}, err
	}
	return record{Kind: "module", Path: final, Hash: hash, Bytes: uint64(before.Size()), Mode: uint32(before.Mode().Perm()), Target: identity}, nil
}

func snapshotModules(ctx context.Context, p *heldProcess, private receipt, rawBytes *uint64) ([]record, error) {
	if err := p.alive(); err != nil {
		return nil, err
	}
	handles, err := moduleHandles(p.handle)
	if err != nil {
		return nil, err
	}
	rows := make([]record, 0, len(handles))
	system, err := windows.GetSystemDirectory()
	if err != nil {
		return nil, err
	}
	if err := canonicalDirectory(system); err != nil {
		return nil, err
	}
	var osBytes uint64
	osCount := 0
	known := map[string]record{}
	for _, row := range private.Records {
		known[strings.ToLower(filepath.Join(private.Root, filepath.FromSlash(row.Path)))] = row
	}
	seen := map[string]bool{}
	for _, handle := range handles {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		row, err := snapshotModule(ctx, p.handle, handle, system, known, &osBytes, &osCount)
		if err != nil {
			return nil, err
		}
		key := strings.ToLower(row.Path)
		if seen[key] {
			return nil, errors.New("duplicate module identity")
		}
		seen[key] = true
		if err := reserve(rawBytes, uint64(len(row.Path)+len(row.Hash)+len(row.Target)+128), maxRawBytes); err != nil {
			return nil, err
		}
		rows = append(rows, row)
	}
	if err := p.alive(); err != nil {
		return nil, err
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Path < rows[j].Path })
	return rows, nil
}

func heldModules(ctx context.Context, p *heldProcess, private receipt) ([]record, error) {
	var rawBytes uint64
	first, err := snapshotModules(ctx, p, private, &rawBytes)
	if err != nil {
		return nil, err
	}
	second, err := snapshotModules(ctx, p, private, &rawBytes)
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(first, second) {
		return nil, errors.New("held module snapshots differ")
	}
	return admitModules(first, private)
}

func admitModules(rows []record, private receipt) ([]record, error) {
	system, err := windows.GetSystemDirectory()
	if err != nil {
		return nil, err
	}
	if err := canonicalDirectory(system); err != nil {
		return nil, err
	}
	known := map[string]record{}
	for _, row := range private.Records {
		known[strings.ToLower(filepath.Join(private.Root, filepath.FromSlash(row.Path)))] = row
	}
	var osRows []record
	var total uint64
	for _, row := range rows {
		captured, ok := known[strings.ToLower(row.Path)]
		if ok {
			if err := sameModuleBytes(row, captured); err != nil {
				return nil, err
			}
			continue
		}
		if err := admitOSRecord(row, system, len(osRows), &total); err != nil {
			return nil, err
		}
		row.Kind = "os"
		osRows = append(osRows, row)
	}
	return osRows, nil
}

func admitModuleRead(path, system string, known map[string]record, total *uint64, count *int) error {
	if err := canonicalRegular(path); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if row, ok := known[strings.ToLower(path)]; ok {
		if info.Size() < 0 || uint64(info.Size()) != row.Bytes {
			return errors.New("private module size mismatch")
		}
		return nil
	}
	if !strings.EqualFold(filepath.Dir(path), system) || !strings.EqualFold(filepath.Ext(path), ".dll") {
		return errors.New("module outside fixed private/System32 policy")
	}
	if *count >= maxOSModules || info.Size() < 0 || info.Size() > maxOSFileBytes {
		return errors.New("system module limit exceeded before read")
	}
	if err := reserve(total, uint64(info.Size()), maxOSBytes); err != nil {
		return err
	}
	*count++
	return nil
}

func snapshotModule(ctx context.Context, process, module windows.Handle, system string, known map[string]record, total *uint64, count *int) (record, error) {
	path, err := modulePath(process, module)
	if err != nil {
		return record{}, err
	}
	if err := admitModuleRead(path, system, known, total, count); err != nil {
		return record{}, err
	}
	return moduleRecord(ctx, path)
}

func sameModuleBytes(actual, expected record) error {
	if actual.Hash != expected.Hash || actual.Bytes != expected.Bytes || actual.Target != expected.Target || actual.Mode != expected.Mode {
		return errors.New("private loaded module differs from inventory")
	}
	return nil
}

func admitOSRecord(row record, system string, count int, total *uint64) error {
	if !strings.EqualFold(filepath.Dir(row.Path), system) || !strings.EqualFold(filepath.Ext(row.Path), ".dll") {
		return errors.New("loaded module outside fixed private/System32 policy")
	}
	if count >= maxOSModules || row.Bytes > maxOSFileBytes {
		return errors.New("system module limit exceeded")
	}
	return reserve(total, row.Bytes, maxOSBytes)
}

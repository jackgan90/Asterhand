#requires -Version 5.1
[CmdletBinding()]
param(
    [Parameter(Mandatory)][string]$Executable,
    [Parameter(Mandatory)][string]$Icon
)
$ErrorActionPreference = 'Stop'
$Executable = $ExecutionContext.SessionState.Path.GetUnresolvedProviderPathFromPSPath($Executable)
$Icon = $ExecutionContext.SessionState.Path.GetUnresolvedProviderPathFromPSPath($Icon)

if (-not ('Asterhand.Build.IconResources' -as [type])) {
    Add-Type -TypeDefinition @'
using System;
using System.IO;
using System.ComponentModel;
using System.Collections.Generic;
using System.Runtime.InteropServices;

namespace Asterhand.Build {
    public static class IconResources {
        private delegate bool EnumName(IntPtr module, IntPtr type, IntPtr name, IntPtr parameter);
        [DllImport("kernel32.dll", CharSet = CharSet.Unicode, SetLastError = true)]
        private static extern IntPtr BeginUpdateResourceW(string file, bool deleteExisting);
        [DllImport("kernel32.dll", SetLastError = true)]
        private static extern bool UpdateResourceW(IntPtr update, IntPtr type, IntPtr name, ushort language, byte[] data, uint length);
        [DllImport("kernel32.dll", SetLastError = true)]
        private static extern bool EndUpdateResourceW(IntPtr update, bool discard);
        [DllImport("kernel32.dll", CharSet = CharSet.Unicode, SetLastError = true)]
        private static extern IntPtr LoadLibraryExW(string file, IntPtr reserved, uint flags);
        [DllImport("kernel32.dll", SetLastError = true)]
        private static extern bool FreeLibrary(IntPtr module);
        [DllImport("kernel32.dll", SetLastError = true)]
        private static extern IntPtr FindResourceExW(IntPtr module, IntPtr type, IntPtr name, ushort language);
        [DllImport("kernel32.dll", SetLastError = true)]
        private static extern IntPtr LoadResource(IntPtr module, IntPtr resource);
        [DllImport("kernel32.dll", SetLastError = true)]
        private static extern IntPtr LockResource(IntPtr resource);
        [DllImport("kernel32.dll", SetLastError = true)]
        private static extern uint SizeofResource(IntPtr module, IntPtr resource);
        [DllImport("kernel32.dll", CharSet = CharSet.Unicode, SetLastError = true)]
        private static extern bool EnumResourceNamesW(IntPtr module, IntPtr type, EnumName callback, IntPtr parameter);

        private sealed class Frame {
            internal byte Width, Height, Colors;
            internal ushort Planes, Bits, Id;
            internal byte[] Data;
        }

        private static Frame[] Parse(byte[] bytes) {
            if (bytes.Length < 6 || BitConverter.ToUInt16(bytes, 0) != 0 || BitConverter.ToUInt16(bytes, 2) != 1)
                throw new InvalidDataException("Invalid ICO header.");
            int count = BitConverter.ToUInt16(bytes, 4);
            if (count == 0 || count > 256 || bytes.Length < 6 + count * 16)
                throw new InvalidDataException("Invalid ICO image directory.");
            Frame[] frames = new Frame[count];
            for (int i = 0; i < count; i++) {
                int entry = 6 + i * 16;
                uint size = BitConverter.ToUInt32(bytes, entry + 8);
                uint offset = BitConverter.ToUInt32(bytes, entry + 12);
                if (size == 0 || offset < 6 + count * 16 || (ulong)offset + size > (ulong)bytes.Length)
                    throw new InvalidDataException("ICO frame lies outside the file.");
                Frame frame = new Frame {
                    Width = bytes[entry], Height = bytes[entry + 1], Colors = bytes[entry + 2],
                    Planes = BitConverter.ToUInt16(bytes, entry + 4), Bits = BitConverter.ToUInt16(bytes, entry + 6),
                    Data = new byte[(int)size]
                };
                Buffer.BlockCopy(bytes, (int)offset, frame.Data, 0, (int)size);
                frames[i] = frame;
            }
            return frames;
        }

        private static IntPtr Open(string file) {
            // Data-file/image-resource flags never execute code or load dependencies.
            IntPtr module = LoadLibraryExW(file, IntPtr.Zero, 0x22);
            if (module == IntPtr.Zero) throw new Win32Exception(Marshal.GetLastWin32Error(), "Cannot inspect PE resources.");
            return module;
        }

        private static HashSet<int> ExistingIconIds(string file) {
            IntPtr module = Open(file);
            try {
                HashSet<int> ids = new HashSet<int>();
                EnumName callback = delegate(IntPtr unusedModule, IntPtr unusedType, IntPtr name, IntPtr unusedParameter) {
                    long value = name.ToInt64();
                    if (value > 0 && value <= ushort.MaxValue) ids.Add((int)value);
                    return true;
                };
                if (!EnumResourceNamesW(module, new IntPtr(3), callback, IntPtr.Zero)) {
                    int error = Marshal.GetLastWin32Error();
                    if (error != 1812 && error != 1813) throw new Win32Exception(error, "Cannot enumerate existing icons.");
                }
                GC.KeepAlive(callback);
                return ids;
            } finally { FreeLibrary(module); }
        }

        private static byte[] Group(Frame[] frames) {
            using (MemoryStream stream = new MemoryStream())
            using (BinaryWriter writer = new BinaryWriter(stream)) {
                writer.Write((ushort)0);
                writer.Write((ushort)1);
                writer.Write((ushort)frames.Length);
                foreach (Frame frame in frames) {
                    writer.Write(frame.Width); writer.Write(frame.Height);
                    writer.Write(frame.Colors); writer.Write((byte)0);
                    writer.Write(frame.Planes); writer.Write(frame.Bits);
                    writer.Write((uint)frame.Data.Length); writer.Write(frame.Id);
                }
                return stream.ToArray();
            }
        }

        private static void Put(IntPtr update, int type, int id, byte[] data) {
            if (!UpdateResourceW(update, new IntPtr(type), new IntPtr(id), 0, data, (uint)data.Length))
                throw new Win32Exception(Marshal.GetLastWin32Error(), "Cannot update icon resource.");
        }

        public static byte[] Read(string file, int type, int id) {
            IntPtr module = Open(file);
            try {
                IntPtr resource = FindResourceExW(module, new IntPtr(type), new IntPtr(id), 0);
                if (resource == IntPtr.Zero) throw new Win32Exception(Marshal.GetLastWin32Error(), "Missing PE resource.");
                uint size = SizeofResource(module, resource);
                IntPtr loaded = LoadResource(module, resource);
                IntPtr pointer = LockResource(loaded);
                if (size == 0 || pointer == IntPtr.Zero) throw new InvalidDataException("Empty PE resource.");
                byte[] data = new byte[(int)size];
                Marshal.Copy(pointer, data, 0, data.Length);
                return data;
            } finally { FreeLibrary(module); }
        }

        private static void Verify(string file, Frame[] frames, byte[] group) {
            if (!Equal(Read(file, 14, 1), group)) throw new InvalidDataException("Embedded icon group did not verify.");
            foreach (Frame frame in frames)
                if (!Equal(Read(file, 3, frame.Id), frame.Data)) throw new InvalidDataException("Embedded icon frame did not verify.");
        }

        private static bool Equal(byte[] left, byte[] right) {
            if (left.Length != right.Length) return false;
            for (int i = 0; i < left.Length; i++) if (left[i] != right[i]) return false;
            return true;
        }

        public static void Apply(string executable, string icon) {
            Frame[] frames = Parse(File.ReadAllBytes(icon));
            HashSet<int> ids = ExistingIconIds(executable);
            int nextId = 1;
            foreach (Frame frame in frames) {
                while (ids.Contains(nextId)) nextId++;
                if (nextId > ushort.MaxValue) throw new InvalidDataException("No free icon resource identifiers.");
                frame.Id = (ushort)nextId++;
            }
            byte[] group = Group(frames);
            string working = Path.Combine(Path.GetDirectoryName(executable), ".asterhand-icon-" + Guid.NewGuid().ToString("N") + ".tmp");
            File.Copy(executable, working, false);
            try {
                UpdateCopy(working, frames, group);
                Verify(working, frames, group);
                // Replacement occurs only after the complete resource transaction and readback pass.
                File.Replace(working, executable, null);
            } finally {
                if (File.Exists(working)) File.Delete(working);
            }
        }

        private static void UpdateCopy(string working, Frame[] frames, byte[] group) {
            IntPtr update = BeginUpdateResourceW(working, false);
            if (update == IntPtr.Zero) throw new Win32Exception(Marshal.GetLastWin32Error(), "Cannot begin icon update.");
            try {
                foreach (Frame frame in frames) Put(update, 3, frame.Id, frame.Data);
                Put(update, 14, 1, group);
            } catch {
                EndUpdateResourceW(update, true);
                throw;
            }
            if (!EndUpdateResourceW(update, false))
                throw new Win32Exception(Marshal.GetLastWin32Error(), "Cannot commit icon update.");
        }
    }
}
'@
}
[Asterhand.Build.IconResources]::Apply($Executable, $Icon)
Write-Host "Embedded and verified application icon: $Executable"

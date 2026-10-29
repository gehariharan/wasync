; Per-user installation. Runtime data is deliberately outside {app}.
[Setup]
AppId={{A3D912C0-B5BF-46F8-85F4-78E1FEF01FCA}
AppName=wasync
AppVersion={#AppVersion}
AppPublisher=Hariharan Ragunathan
AppPublisherURL=https://github.com/gehariharan/WhatsappTuiCli
DefaultDirName={localappdata}\Programs\wasync
DefaultGroupName=wasync
PrivilegesRequired=lowest
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible
MinVersion=10.0
OutputDir={#OutputDir}
OutputBaseFilename={#OutputName}
Compression=lzma2
SolidCompression=yes
WizardStyle=modern
ChangesEnvironment=yes
LicenseFile={#SourceDir}\LICENSE
UninstallDisplayIcon={app}\wasync.exe
CloseApplications=yes

[Tasks]
Name: addtopath; Description: "Add wasync to your user PATH (open a new terminal after installation)"; Flags: checkedonce
; AXI for agents is a choice and starts unticked. Silent installs can opt in with /TASKS or /MERGETASKS="installaxi".
Name: installaxi; Description: "Also install whatsapp-axi, the tool AI agents use with wasync (AXI for agents). Keep the PATH option above ticked so agents can find it"; Flags: unchecked

[Files]
Source: "{#SourceDir}\*"; DestDir: "{app}"; Excludes: "whatsapp-axi.exe"; Flags: ignoreversion recursesubdirs createallsubdirs
Source: "{#SourceDir}\whatsapp-axi.exe"; DestDir: "{app}"; Flags: ignoreversion; Tasks: installaxi

[Icons]
Name: "{group}\wasync"; Filename: "{app}\wasync.exe"; WorkingDir: "{app}"

; Offered on the last page, unticked, and never run by a silent install. They change
; your agents' configuration, so only a tick here runs them (as you, without administrator access).
[Run]
Filename: "{app}\whatsapp-axi.exe"; Parameters: "setup skill"; Description: "Add the whatsapp-axi skill to my AI agents (runs whatsapp-axi setup skill)"; Flags: postinstall skipifsilent unchecked runhidden; Tasks: installaxi
Filename: "{app}\whatsapp-axi.exe"; Parameters: "setup hooks"; Description: "Add whatsapp-axi's session hooks to my AI agents (runs whatsapp-axi setup hooks)"; Flags: postinstall skipifsilent unchecked runhidden; Tasks: installaxi

[Code]
function HasPath(Value, Item: String): Boolean;
begin
  Result := Pos(';' + Lowercase(Item) + ';', ';' + Lowercase(Value) + ';') > 0;
end;

procedure CurStepChanged(CurStep: TSetupStep);
var Value, AppPath: String;
begin
  if (CurStep = ssPostInstall) and WizardIsTaskSelected('addtopath') then begin
    AppPath := ExpandConstant('{app}');
    RegQueryStringValue(HKCU, 'Environment', 'Path', Value);
    if not HasPath(Value, AppPath) then begin
      if (Value <> '') and (Copy(Value, Length(Value), 1) <> ';') then Value := Value + ';';
      if not RegWriteExpandStringValue(HKCU, 'Environment', 'Path', Value + AppPath) then
        RaiseException('Could not update user PATH.');
      RegWriteStringValue(HKCU, 'Software\wasync', 'InstallerPath', AppPath);
    end;
  end;
end;

procedure CurUninstallStepChanged(CurUninstallStep: TUninstallStep);
var Value, OwnedPath, Item, NewValue: String; Parts: TArrayOfString; I: Integer;
begin
  if CurUninstallStep = usPostUninstall then begin
    if RegQueryStringValue(HKCU, 'Software\wasync', 'InstallerPath', OwnedPath) then begin
      if RegQueryStringValue(HKCU, 'Environment', 'Path', Value) then begin
        Parts := StringSplit(Value, [';'], stAll);
        NewValue := '';
        for I := 0 to GetArrayLength(Parts) - 1 do begin
          Item := Parts[I];
          if CompareText(Item, OwnedPath) <> 0 then begin
            if NewValue <> '' then NewValue := NewValue + ';';
            NewValue := NewValue + Item;
          end;
        end;
        RegWriteExpandStringValue(HKCU, 'Environment', 'Path', NewValue);
      end;
      RegDeleteValue(HKCU, 'Software\wasync', 'InstallerPath');
    end;
  end;
end;

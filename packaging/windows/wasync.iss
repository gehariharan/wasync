; Per-user installation. Runtime data is deliberately outside {app}.
; Saved as UTF-8 with a byte order mark, so Inno Setup reads the byline's middle dot.
; Every page is Inno Setup's own; only its words and pictures are set here.
[Setup]
AppId={{A3D912C0-B5BF-46F8-85F4-78E1FEF01FCA}
AppName=wasync
AppVersion={#AppVersion}
AppPublisher=Hariharan Ragunathan
AppPublisherURL=https://gehariharan.com
AppSupportURL=https://github.com/gehariharan/wasync
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
DisableWelcomePage=no
ChangesEnvironment=yes
; The note that wasync is not WhatsApp, above the MIT licence (scripts/package-windows.ps1 joins them).
LicenseFile={#LicensePage}
SetupIconFile={#PackagingDir}\wasync.ico
; Logo D centred on the side picture, at each display scale from 100% to 250%.
WizardImageFile={#PackagingDir}\wizard-0.bmp,{#PackagingDir}\wizard-1.bmp,{#PackagingDir}\wizard-2.bmp,{#PackagingDir}\wizard-3.bmp,{#PackagingDir}\wizard-4.bmp,{#PackagingDir}\wizard-5.bmp,{#PackagingDir}\wizard-6.bmp
WizardSmallImageFile={#PackagingDir}\wizard-small-0.bmp,{#PackagingDir}\wizard-small-1.bmp,{#PackagingDir}\wizard-small-2.bmp,{#PackagingDir}\wizard-small-3.bmp,{#PackagingDir}\wizard-small-4.bmp,{#PackagingDir}\wizard-small-5.bmp,{#PackagingDir}\wizard-small-6.bmp
UninstallDisplayIcon={app}\wasync-desktop.exe
CloseApplications=yes

[Messages]
WelcomeLabel1=Welcome to wasync
WelcomeLabel2=wasync keeps the WhatsApp chats you choose on this computer, for you and your AI agents.%n%nby @gehariharan · gehariharan.com
FinishedHeadingLabel=wasync is installed
FinishedLabelNoIcons=wasync opens in its own window and keeps running in the notification area when you close it.%n%nby @gehariharan · gehariharan.com
FinishedLabel=wasync opens in its own window and keeps running in the notification area when you close it. Open it again from the Start menu.%n%nby @gehariharan · gehariharan.com

[Tasks]
Name: addtopath; Description: "Add wasync and wasync-axi, the tool AI agents use with wasync, to your user PATH (open a new terminal after installation)"; Flags: checkedonce

[Files]
; Everything, including wasync-axi (AXI for agents), which does nothing until an agent is
; connected, and whatsapp-axi.exe, its old name, kept so existing agent settings keep working.
Source: "{#SourceDir}\*"; DestDir: "{app}"; Flags: ignoreversion recursesubdirs createallsubdirs

[Icons]
Name: "{group}\wasync"; Filename: "{app}\wasync-desktop.exe"; WorkingDir: "{app}"

[Registry]
; The wasync app's "Start when I log in" item points into {app}; remove it with the app.
Root: HKCU; Subkey: "Software\Microsoft\Windows\CurrentVersion\Run"; ValueName: "wasync-app"; ValueType: none; Flags: uninsdeletevalue dontcreatekey

; The first entry is Setup's own "run the program" tick box, ticked. The others are offered
; on the last page, unticked, and never run by a silent install. They change your agents'
; configuration, so only a tick here runs them (as you, without administrator access).
[Run]
Filename: "{app}\wasync-desktop.exe"; Description: "Open wasync now"; Flags: postinstall nowait skipifsilent
Filename: "{app}\wasync-axi.exe"; Parameters: "setup skill"; Description: "Add the wasync-axi skill to my AI agents (runs wasync-axi setup skill)"; Flags: postinstall skipifsilent unchecked runhidden
Filename: "{app}\wasync-axi.exe"; Parameters: "setup hooks"; Description: "Add wasync-axi's session hooks to my AI agents (runs wasync-axi setup hooks)"; Flags: postinstall skipifsilent unchecked runhidden

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

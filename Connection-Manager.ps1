param([string]$ConfigPath,[string]$Executable)
$ErrorActionPreference='Stop'
Add-Type -AssemblyName System.Windows.Forms
Add-Type -AssemblyName System.Drawing
$script:doc=@{version=1;connections=@{}}
$script:dirty=$false;$script:loading=$false;$script:selected=$null;$script:testProcess=$null;$script:testPath=$null
function Hash-Config { if(Test-Path -LiteralPath $ConfigPath){return (Get-FileHash -LiteralPath $ConfigPath -Algorithm SHA256).Hash}; return '' }
function Confirm-Discard { return (!$script:dirty -or [Windows.Forms.MessageBox]::Show('Discard unsaved changes?','AMP Connections','YesNo') -eq 'Yes') }
function Load-Config {
 $script:loading=$true
 try {
  $doc=@{version=1;connections=@{}};$before=Hash-Config
  if(Test-Path -LiteralPath $ConfigPath){$raw=Get-Content -LiteralPath $ConfigPath -Raw|ConvertFrom-Json;if($raw.version -ne 1){throw 'Unsupported configuration version.'};foreach($p in $raw.connections.PSObject.Properties){$doc.connections[$p.Name]=$p.Value}}
  if((Hash-Config) -ne $before){throw 'Configuration changed while reading. Reload again.'}
  $script:doc=$doc;$script:hash=$before;$script:selected=$null
  $list.Items.Clear();foreach($n in ($doc.connections.Keys|Sort-Object)){$null=$list.Items.Add($n)}
  foreach($f in $fields.Values){$f.Clear()};$fields.Timeout.Text='30';$fields.Authentication.Text='service_login';$script:dirty=$false
 } finally {$script:loading=$false}
}
$form=New-Object Windows.Forms.Form
$form.Text='AMP Connections';$form.Size=New-Object Drawing.Size(700,660);$form.StartPosition='CenterScreen';$form.MinimumSize=$form.Size
$list=New-Object Windows.Forms.ListBox;$list.SetBounds(15,15,180,390);$form.Controls.Add($list)
$fields=@{};$labels=@('Name','HTTPS URL','Username','Credential','CA file (optional)','Timeout','Instance ID (optional)','Authentication')
for($i=0;$i -lt $labels.Count;$i++){
 $l=New-Object Windows.Forms.Label;$l.Text=$labels[$i];$l.SetBounds(210,(20+60*$i),440,20);$form.Controls.Add($l)
 $t=New-Object Windows.Forms.TextBox;$t.SetBounds(210,(42+60*$i),440,25);$t.Add_TextChanged({if(!$script:loading){$script:dirty=$true}});$form.Controls.Add($t);$fields[$labels[$i]]=$t
}
$fields['Credential'].UseSystemPasswordChar=$true
$status=New-Object Windows.Forms.Label;$status.SetBounds(15,575,650,40);$form.Controls.Add($status)
$buttons=@{}
function Button($text,$x,$action){$b=New-Object Windows.Forms.Button;$b.Text=$text;$b.SetBounds($x,535,115,30);$b.Add_Click($action);$form.Controls.Add($b);$buttons[$text]=$b}
$list.Add_SelectedIndexChanged({
 if($script:loading -or !$list.SelectedItem){return}
 if(! (Confirm-Discard)){$script:loading=$true;try{$list.SelectedItem=$script:selected}finally{$script:loading=$false};return}
 $script:loading=$true
 try {
  $script:selected=[string]$list.SelectedItem;$c=$script:doc.connections[$script:selected]
  $fields.Name.Text=$script:selected;$fields['HTTPS URL'].Text=$c.url;$fields.Username.Text=$c.username
  $fields['CA file (optional)'].Text=[string]$c.caCert;$fields.Timeout.Text=$(if($c.timeoutSeconds){[string]$c.timeoutSeconds}else{'30'})
  $fields['Instance ID (optional)'].Text=[string]$c.instanceId
  $fields['Credential'].Clear();$secret=$c.serviceTokenDpapi;$fields.Authentication.Text='service_login';if($c.passwordDpapi){$secret=$c.passwordDpapi;$fields.Authentication.Text='password'};$s=ConvertTo-SecureString $secret;$cred=New-Object Management.Automation.PSCredential($c.username,$s)
  $fields['Credential'].Text=$cred.GetNetworkCredential().Password;$status.Text='';$script:dirty=$false
 } catch {$status.Text='Cannot decrypt token; enter it again.'} finally {$script:loading=$false}
})
function Draft {
 $name=$fields.Name.Text.Trim();$uri=$null;$timeout=0
 if(!$name -or !$fields.Username.Text.Trim() -or !$fields['Credential'].Text){throw 'Name, username and token are required.'}
 if(![Uri]::TryCreate($fields['HTTPS URL'].Text.Trim(),[UriKind]::Absolute,[ref]$uri) -or $uri.Scheme -ne 'https' -or $uri.UserInfo -or $uri.Query -or $uri.Fragment){throw 'Enter an HTTPS URL without credentials, query or fragment.'}
 if(![int]::TryParse($fields.Timeout.Text,[ref]$timeout) -or $timeout -lt 1 -or $timeout -gt 300){throw 'Timeout must be 1-300 seconds.'}
 if($script:doc.connections.ContainsKey($name) -and $name -ne $script:selected){throw 'Connection name already exists.'}
 $ca=$fields['CA file (optional)'].Text.Trim();if($ca -and !(Test-Path -LiteralPath $ca -PathType Leaf)){throw 'CA file does not exist.'}
 $mode=$fields.Authentication.Text.Trim();if($mode -notin @('service_login','password')){throw 'Authentication must be service_login or password.'};$token=$fields['Credential'].Text;if($mode -eq 'service_login'){$token=$token.Trim()};if($mode -eq 'service_login' -and $token.Contains(':')){$parts=$token.Split(':',2);if($parts[0] -ne $fields.Username.Text.Trim()){throw 'Token username does not match.'};$token=$parts[1]}
 $protected=ConvertFrom-SecureString (ConvertTo-SecureString $token -AsPlainText -Force)
 $instance=$fields['Instance ID (optional)'].Text.Trim();$instanceGuid=[guid]::Empty
 if($instance -and ![guid]::TryParseExact($instance,'D',[ref]$instanceGuid)){throw 'Instance ID must be a GUID, or empty for ADS.'}
 if($mode -eq 'password' -and !$instance){throw 'Password authentication requires an instance ID.'};$record=@{url=$uri.AbsoluteUri.TrimEnd('/');username=$fields.Username.Text.Trim();caCert=$ca;timeoutSeconds=$timeout;instanceId=$instance};if($mode -eq 'password'){$record.passwordDpapi=$protected}else{$record.serviceTokenDpapi=$protected};return @{name=$name;record=$record}
}
function Save-Document($document) {
 $dir=Split-Path -Parent $ConfigPath;$null=New-Item -ItemType Directory -Force -Path $dir
 $lock=$null;$tmp=Join-Path $dir ([guid]::NewGuid().ToString()+'.tmp')
 try {
  $lock=[IO.File]::Open(($ConfigPath+'.lock'),[IO.FileMode]::OpenOrCreate,[IO.FileAccess]::ReadWrite,[IO.FileShare]::None)
  if((Hash-Config) -ne $script:hash){throw 'Configuration changed. Reload before saving.'}
  [IO.File]::WriteAllText($tmp,($document|ConvertTo-Json -Depth 8),[Text.UTF8Encoding]::new($false))
  if(Test-Path -LiteralPath $ConfigPath){[IO.File]::Replace($tmp,$ConfigPath,($ConfigPath+'.backup'))}else{[IO.File]::Move($tmp,$ConfigPath)}
  $script:hash=Hash-Config
 } finally {if($lock){$lock.Dispose()};if(Test-Path -LiteralPath $tmp){Remove-Item -LiteralPath $tmp}}
}
function Copy-Document { $copy=@{version=1;connections=@{}};foreach($key in $script:doc.connections.Keys){$copy.connections[$key]=$script:doc.connections[$key]};return $copy }
function End-Test {
 if($script:testProcess){if(!$script:testProcess.HasExited){$script:testProcess.Kill();$null=$script:testProcess.WaitForExit(2000)};$script:testProcess.Dispose();$script:testProcess=$null}
 if($script:testPath -and (Test-Path -LiteralPath $script:testPath)){Remove-Item -LiteralPath $script:testPath};$script:testPath=$null;$buttons.Test.Enabled=$true;$timer.Stop()
}
$timer=New-Object Windows.Forms.Timer;$timer.Interval=150
$timer.Add_Tick({try{if($script:testProcess.HasExited){$ok=$script:testProcess.ExitCode -eq 0;End-Test;$status.Text=$(if($ok){'Connection successful (draft not saved).'}else{'Connection failed; check address, token, permissions and certificate trust.'})}elseif(([DateTime]::UtcNow-$script:testStarted).TotalSeconds -gt 12){End-Test;$status.Text='Connection test timed out (12 seconds).'}}catch{End-Test;$status.Text='Connection test failed.'}})
Button 'New' 15 {if(!(Confirm-Discard)){return};$script:loading=$true;try{$script:selected=$null;$list.ClearSelected();foreach($f in $fields.Values){$f.Clear()};$fields.Timeout.Text='30';$fields.Authentication.Text='service_login';$script:dirty=$false;$status.Text='New connection; Save to persist.'}finally{$script:loading=$false}}
Button 'Save' 140 {try{$d=Draft;$copy=Copy-Document;if($script:selected -and $script:selected -ne $d.name){$copy.connections.Remove($script:selected)};$copy.connections[$d.name]=$d.record;Save-Document $copy;Load-Config;$status.Text='Saved with Windows DPAPI.'}catch{$status.Text=$_.Exception.Message}}
Button 'Test' 265 {
 try {
  $d=Draft;$script:testPath=Join-Path ([IO.Path]::GetTempPath()) ([guid]::NewGuid().ToString()+'.json')
  $d.record.timeoutSeconds=[Math]::Min(10,$d.record.timeoutSeconds)
  [IO.File]::WriteAllText($script:testPath,(@{version=1;connections=@{test=$d.record}}|ConvertTo-Json -Depth 8),[Text.UTF8Encoding]::new($false))
  $info=New-Object Diagnostics.ProcessStartInfo;$info.FileName=$Executable;$info.Arguments='test-connection test';$info.UseShellExecute=$false;$info.CreateNoWindow=$true;$info.RedirectStandardOutput=$true;$info.RedirectStandardError=$true;$info.EnvironmentVariables['AMP_MCP_CONFIG']=$script:testPath
  $script:testProcess=[Diagnostics.Process]::Start($info);$script:testStarted=[DateTime]::UtcNow;$buttons.Test.Enabled=$false;$status.Text='Testing connection...';$timer.Start()
 } catch {End-Test;$status.Text='Cannot start connection test.'}
}
Button 'Delete' 390 {try{if(!$script:selected -or !(Confirm-Discard)){return};if([Windows.Forms.MessageBox]::Show('Delete this local connection?','AMP Connections','YesNo') -ne 'Yes'){return};$copy=Copy-Document;$copy.connections.Remove($script:selected);Save-Document $copy;Load-Config;$status.Text='Deleted local connection.'}catch{$status.Text=$_.Exception.Message}}
Button 'Reload' 515 {try{if(!(Confirm-Discard)){return};Load-Config;$status.Text='Reloaded.'}catch{$status.Text='Cannot read configuration.'}}
$form.Add_FormClosing({param($sender,$eventArgs)if(!(Confirm-Discard)){$eventArgs.Cancel=$true;return};End-Test;foreach($f in $fields.Values){$f.Clear()}})
Load-Config
$null=$form.ShowDialog()

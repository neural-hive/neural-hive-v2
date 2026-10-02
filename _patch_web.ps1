$ErrorActionPreference='Stop'; $p='web/chat.js'; $s=[IO.File]::ReadAllText((Resolve-Path $p))
$i=$s.IndexOf('  function resultMeta(r){'); $j=$s.IndexOf('  return box; }',$i); if($i -lt 0 -or $j -lt 0){ throw 'meta block not found' }; $tail=$s.Substring($j+16)
$blk=[IO.File]::ReadAllText((Resolve-Path '_blk.txt')).Replace([char]126,[char]34).TrimEnd([char]13,[char]10)
$s=$s.Substring(0,$i)+$blk+$tail
$oldn='if(m.meta){ ans.appendChild(m.meta); }'; $newn='var metaNode=(m.metaData!=null)?renderMeta(m.metaData):((m.meta&&m.meta.nodeType===1)?m.meta:null); if(metaNode){ ans.appendChild(metaNode); }'; if($s.IndexOf($oldn) -lt 0){ throw 'node line not found' }; $s=$s.Replace($oldn,$newn)
$olds='meta:resultMeta(d)'; $news='metaData:toMetaData(d)'; if($s.IndexOf($olds) -lt 0){ throw 'store line not found' }; $s=$s.Replace($olds,$news)
[IO.File]::WriteAllText((Resolve-Path 'web/chat.js'), $s)
Write-Host ('patched len ' + $s.Length + ' toMetaData ' + $s.Contains('function toMetaData') + ' renderMeta ' + $s.Contains('function renderMeta') + ' noResultMeta ' + (-not $s.Contains('resultMeta')))

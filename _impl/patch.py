import sys 
from pathlib import Path 
def repl_lines(path, start, end, newfile): 
    lines = Path(path).read_text(encoding='utf-8').splitlines(True) 
    new = Path(newfile).read_text(encoding='utf-8').splitlines(True) 
    if new and not new[-1].endswith(chr(10)): new[-1] = new[-1] + chr(10) 
    lines[start-1:end] = new 
    Path(path).write_text(''.join(lines), encoding='utf-8', newline='') 
    print('replaced lines %d-%d in %s' % (start, end, path)) 
def repl_str(path, oldfile, newfile): 
    s = Path(path).read_text(encoding='utf-8') 
    old = Path(oldfile).read_text(encoding='utf-8') 
    new = Path(newfile).read_text(encoding='utf-8') 
    if old not in s: 
        print('ANCHOR NOT FOUND in ' + path); sys.exit(2) 
    Path(path).write_text(s.replace(old, new, 1), encoding='utf-8', newline='') 
    print('patched ' + path) 
import patchdata 

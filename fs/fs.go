package fs

import (
	"fmt"
	"io"
	"io/fs"
	"os"
)

func DirExist(dir string) bool {
	_, err := os.Stat(dir)
	return !os.IsNotExist(err)
}

func ReadByte(dir string) ([]byte, error) {
	b, err := os.ReadFile(dir)
	if err != nil {
		return nil, err
	}
	return b, nil
}

func ReadString(dir string) (string, error) {
	b, err := ReadByte(dir)
	return string(b), err
}

func readDir(dirname string) ([]fs.FileInfo, error) {
	f, err := os.Open(dirname)
	if err != nil {
		return nil, err
	}
	list, err := f.Readdir(-1)
	f.Close()
	if err != nil {
		return nil, err
	}
	return list, nil
}

func ReadAllFiles(dir string, fn interface{}) {
	items, _ := readDir(dir)
	for _, item := range items {
		if item.IsDir() {
			subitems, _ := readDir(dir + "/" + item.Name())
			for _, subitem := range subitems {
				if !subitem.IsDir() {
					if subitem.Name() == ".DS_Store" {
						continue
					}
					fn.(func(string, string))(item.Name(), subitem.Name())
				}
			}
		} else {
			if item.Name() == ".DS_Store" {
				continue
			}
			fn.(func(string, string))("", item.Name())
		}
	}
}

func GetFilesList(dir string) []string {
	ret := []string{}
	dirRead, _ := os.Open(dir)
	dirFiles, _ := dirRead.Readdir(0)
	for index := range dirFiles {
		fileHere := dirFiles[index]
		ret = append(ret, fileHere.Name())
	}
	return ret
}

var BUFFERSIZE int64

func CopyDir(src, dst string, BUFFERSIZE int64) error {
	sourceFileStat, err := os.Stat(src)
	if err != nil {
		return err
	}

	if !sourceFileStat.Mode().IsRegular() {
		return fmt.Errorf(src + " is not a regular file.")
	}

	source, err := os.Open(src)
	if err != nil {
		return err
	}
	defer source.Close()

	_, err = os.Stat(dst)
	if err == nil {
		return fmt.Errorf("File %s already exists.", dst)
	}

	destination, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer destination.Close()

	buf := make([]byte, BUFFERSIZE)
	for {
		n, err := source.Read(buf)
		if err != nil && err != io.EOF {
			return err
		}
		if n == 0 {
			break
		}

		if _, err := destination.Write(buf[:n]); err != nil {
			return err
		}
	}
	return err
}

func RemoveContents(dir string, ignore []string) {
	for _, name := range GetFilesList(dir) {
		notOk := false
		for _, n := range ignore {
			if name == n {
				notOk = true
				break
			}
		}
		if notOk {
			continue
		}
		os.RemoveAll(dir + "/" + name)
	}
}

func ReadDir(dirname string) ([]fs.FileInfo, error) {
	f, err := os.Open(dirname)
	if err != nil {
		return nil, err
	}
	list, err := f.Readdir(-1)
	f.Close()
	if err != nil {
		return nil, err
	}
	return list, nil
}

func Exists(filePath string) bool {
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		return false
	}

	return true
}

func CreateIfNotExists(dir string, perm os.FileMode) error {
	if Exists(dir) {
		return nil
	}

	if err := os.MkdirAll(dir, perm); err != nil {
		return fmt.Errorf("failed to create directory: '%s', error: '%s'", dir, err.Error())
	}

	return nil
}

func CopySymLink(source, dest string) error {
	link, err := os.Readlink(source)
	if err != nil {
		return err
	}

	return os.Symlink(link, dest)
}

func CutTo(data []byte, size int) [][]byte {
	var ret [][]byte
	dLen := len(data) / size
	if dLen == 0 {
		dLen = 1
		ret = append(ret, data)
	} else {
		for i := 0; i < dLen; i++ {
			data = data[size:]
			if len(data) < size {
				size = len(data)
			}
			ret = append(ret, data[:size])
		}
	}
	return ret
}
